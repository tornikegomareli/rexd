// Package service makes the daemon start at login. On macOS that is a LaunchAgent.
// On other systems the user runs "rexd daemon" under their own init system.
package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tornikegomareli/rexd/internal/fsutil"
)

// Label is the name of rexd's LaunchAgent.
const Label = "io.github.tornikegomareli.rexd"

const (
	// bootoutTimeout covers the daemon stopping its scripts: a few seconds for
	// each to exit, and launchd noticing.
	bootoutTimeout = 20 * time.Second
	pollInterval   = 100 * time.Millisecond
)

// Supported reports whether this system has LaunchAgents.
func Supported() bool { return runtime.GOOS == "darwin" }

// Spec describes the LaunchAgent.
type Spec struct {
	Label string
	// Program is the command and its arguments.
	Program []string
	// Env holds the agent's environment. launchd gives it almost nothing by default.
	Env map[string]string
	// LogPath receives what the program prints outside its own log, such as a panic.
	LogPath string
}

// PlistPath is where a LaunchAgent's file lives.
func PlistPath(label string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

// RenderPlist returns the LaunchAgent's property list. RunAtLoad starts the daemon
// at login, and KeepAlive starts it again if it dies.
func RenderPlist(spec Spec) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	writeKeyString(&b, "\t", "Label", spec.Label)
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, arg := range spec.Program {
		writeString(&b, "\t\t", arg)
	}
	b.WriteString("\t</array>\n")
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n\t<true/>\n")
	if len(spec.Env) > 0 {
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		names := make([]string, 0, len(spec.Env))
		for name := range spec.Env {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			writeKeyString(&b, "\t\t", name, spec.Env[name])
		}
		b.WriteString("\t</dict>\n")
	}
	if spec.LogPath != "" {
		writeKeyString(&b, "\t", "StandardOutPath", spec.LogPath)
		writeKeyString(&b, "\t", "StandardErrorPath", spec.LogPath)
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes()
}

func writeKeyString(b *bytes.Buffer, indent, key, value string) {
	b.WriteString(indent + "<key>")
	xml.EscapeText(b, []byte(key))
	b.WriteString("</key>\n")
	writeString(b, indent, value)
}

func writeString(b *bytes.Buffer, indent, value string) {
	b.WriteString(indent + "<string>")
	xml.EscapeText(b, []byte(value))
	b.WriteString("</string>\n")
}

// Install writes the LaunchAgent and starts it, replacing any earlier one.
func Install(ctx context.Context, spec Spec) error {
	path, err := PlistPath(spec.Label)
	if err != nil {
		return err
	}
	if err := Stop(ctx, spec.Label); err != nil {
		return err
	}
	if spec.LogPath != "" {
		if err := os.MkdirAll(filepath.Dir(spec.LogPath), 0o755); err != nil {
			return err
		}
	}
	if err := fsutil.WriteFileAtomic(path, RenderPlist(spec), 0o644); err != nil {
		return err
	}
	return bootstrap(ctx, path)
}

// Uninstall stops the LaunchAgent and deletes its file. An agent that does not
// exist is not an error.
func Uninstall(ctx context.Context, label string) error {
	if err := Stop(ctx, label); err != nil {
		return err
	}
	path, err := PlistPath(label)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Stop unloads the LaunchAgent and waits until it is gone. launchd then sends the
// daemon SIGTERM, and the daemon stops its scripts.
func Stop(ctx context.Context, label string) error {
	if !Loaded(ctx, label) {
		return ctx.Err()
	}
	// The result does not matter, because the wait below decides.
	_, _ = launchctl(ctx, "bootout", serviceTarget(label))
	deadline := time.Now().Add(bootoutTimeout)
	for Loaded(ctx, label) {
		if time.Now().After(deadline) {
			return fmt.Errorf("launchd did not stop %s in time", label)
		}
		if err := sleep(ctx, pollInterval); err != nil {
			return err
		}
	}
	return ctx.Err() // Loaded is also false when an ended context stopped launchctl.
}

// Loaded reports whether launchd knows the agent.
func Loaded(ctx context.Context, label string) bool {
	_, err := launchctl(ctx, "print", serviceTarget(label))
	return err == nil
}

// bootstrap loads the agent. launchd can still refuse for a moment after a
// bootout with "Input/output error", so a failure gets a few more tries.
func bootstrap(ctx context.Context, path string) error {
	var err error
	for range 10 {
		if _, err = launchctl(ctx, "bootstrap", domainTarget(), path); err == nil {
			return nil
		}
		if sleepErr := sleep(ctx, 500*time.Millisecond); sleepErr != nil {
			return sleepErr
		}
	}
	return err
}

func domainTarget() string {
	return "gui/" + strconv.Itoa(os.Getuid())
}

func serviceTarget(label string) string {
	return domainTarget() + "/" + label
}

func launchctl(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "launchctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("launchctl %s: %s", args[0], strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
