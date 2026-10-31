// Package rex runs the rex command. It is the only way rexd talks to Rex.
package rex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// exitServerUnavailable is the status rex exits with when it cannot reach the server.
const exitServerUnavailable = 4

// ErrNotRunning means the Rex server is not running.
var ErrNotRunning = errors.New("Rex is not running")

// ErrNotFound means the rex binary could not be found.
var ErrNotFound = errors.New("could not find the rex command; install Rex from https://www.superlogical.com/rex, or set REXD_REX to its path")

// Client runs rex commands against one server.
//
// Every command runs with --autostart=false. Rex starts a missing server by
// default, and rexd must never start Rex behind the user's back.
type Client struct {
	// Path is the rex binary.
	Path string
	// Server is passed as -S. Empty means Rex's default server.
	Server string
}

// Version describes the rex binary.
type Version struct {
	Rex         string `json:"rex_version"`
	APIRevision int    `json:"api_revision"`
}

// ConfigError means a Rex config file has errors. Output is what rex reported.
type ConfigError struct {
	Output string
}

func (e *ConfigError) Error() string {
	var problems []string
	for line := range strings.Lines(e.Output) {
		if strings.HasPrefix(line, "error ") {
			problems = append(problems, strings.TrimSpace(strings.TrimPrefix(line, "error ")))
		}
	}
	if len(problems) == 0 {
		return strings.TrimSpace(e.Output)
	}
	return strings.Join(problems, "\n")
}

// Find locates the rex binary: $REXD_REX, then rex on PATH, then the copy the
// macOS app installs.
func Find() (string, error) {
	if path := os.Getenv("REXD_REX"); path != "" {
		return path, nil
	}
	if path, err := exec.LookPath("rex"); err == nil {
		return path, nil
	}
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path := filepath.Join(home, "Library", "Application Support", "com.superlogical.rex",
			"RexServer", "Versions", "Current", "Rex Server.app", "Contents", "MacOS", "rex")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", ErrNotFound
}

// LocalConfigPath returns where Rex reads its config by default. A running
// server knows better, because it can have --config: see ConfigPath.
func LocalConfigPath() (string, error) {
	if path := os.Getenv("REX_CONFIG"); path != "" {
		return path, nil
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "rex", "init.lua"), nil
}

// Command builds a rex command without running it.
func (c Client) Command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.Path, c.args(args...)...)
	cmd.Env = c.environ()
	return cmd
}

func (c Client) args(args ...string) []string {
	all := []string{"--autostart=false"}
	if c.Server != "" {
		all = append(all, "-S", c.Server)
	}
	return append(all, args...)
}

// environ removes the variables a Rex terminal sets, so rexd behaves the same
// inside and outside Rex. REX_SERVER matters most: when its socket is missing,
// rex silently uses the default server, while -S fails as it should.
func (c Client) environ() []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "REX_SERVER", "REX_SESSION", "REX_BLOCK", "REX_CLIENT":
			continue
		}
		env = append(env, kv)
	}
	return env
}

func (c Client) run(ctx context.Context, args ...string) (string, error) {
	cmd := c.Command(ctx, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.String(), nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == exitServerUnavailable {
		return "", ErrNotRunning
	}
	detail := strings.TrimSpace(stderr.String())
	if detail == "" {
		detail = strings.TrimSpace(stdout.String())
	}
	if detail == "" {
		detail = err.Error()
	}
	return "", fmt.Errorf("rex %s: %s", args[0], detail)
}

// Version reports the version of the rex binary. It works without a server.
func (c Client) Version(ctx context.Context) (Version, error) {
	out, err := c.run(ctx, "version", "--json")
	if err != nil {
		return Version{}, err
	}
	var v Version
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return Version{}, fmt.Errorf("reading rex version: %w", err)
	}
	return v, nil
}

// Sessions returns the IDs of the server's sessions, or ErrNotRunning.
func (c Client) Sessions(ctx context.Context) ([]string, error) {
	out, err := c.run(ctx, "ls", "-q", "--short=false")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// ConfigPath asks the running server which config file it reads, or returns ErrNotRunning.
func (c Client) ConfigPath(ctx context.Context) (string, error) {
	out, err := c.run(ctx, "keymap", "--json")
	if err != nil {
		return "", err
	}
	var keymap struct {
		Config struct {
			Path string `json:"path"`
		} `json:"config"`
	}
	if err := json.Unmarshal([]byte(out), &keymap); err != nil {
		return "", fmt.Errorf("reading rex keymap: %w", err)
	}
	if keymap.Config.Path == "" {
		return "", errors.New("rex keymap did not report a config path")
	}
	return keymap.Config.Path, nil
}

// CheckConfig loads the config file at path without changing anything. It works
// without a server. A file with errors returns a *ConfigError.
func (c Client) CheckConfig(ctx context.Context, path string) error {
	cmd := c.Command(ctx, "config", "check", path)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return &ConfigError{Output: string(output)}
	}
	return fmt.Errorf("rex config check: %w: %s", err, strings.TrimSpace(string(output)))
}

// ReloadConfig makes the server read its config file again. It returns
// ErrNotRunning when there is no server, and Rex reads the file when it starts.
func (c Client) ReloadConfig(ctx context.Context) error {
	_, err := c.run(ctx, "config", "reload")
	return err
}

// ScriptArgs returns the arguments that run an event script. The script hears
// every session, including sessions created later.
func (c Client) ScriptArgs(path string) []string {
	return c.args("do", path, "--all")
}

// ScriptEnv returns the environment for an event script. Without an app
// connected, rex needs a session to start from, and --session conflicts with
// --all. REX_SESSION does not, and the script outlives that session.
func (c Client) ScriptEnv(session string) []string {
	return append(c.environ(), "REX_SESSION="+session)
}
