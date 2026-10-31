package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testSpec() Spec {
	return Spec{
		Label:   "io.github.example.test",
		Program: []string{"/Users/me/My Tools/rexd", "daemon"},
		Env: map[string]string{
			"PATH":           "/opt/homebrew/bin:/usr/bin",
			"XDG_STATE_HOME": "/Users/me/state & more <here>",
		},
		LogPath: "/Users/me/state/launchd.log",
	}
}

func TestRenderPlist(t *testing.T) {
	got := string(RenderPlist(testSpec()))
	for _, want := range []string{
		"<key>Label</key>\n\t<string>io.github.example.test</string>",
		"<string>/Users/me/My Tools/rexd</string>\n\t\t<string>daemon</string>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>KeepAlive</key>\n\t<true/>",
		"<key>PATH</key>\n\t\t<string>/opt/homebrew/bin:/usr/bin</string>",
		"<string>/Users/me/state &amp; more &lt;here&gt;</string>",
		"<key>StandardErrorPath</key>\n\t<string>/Users/me/state/launchd.log</string>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plist has no %q:\n%s", want, got)
		}
	}
}

func TestRenderPlistIsStable(t *testing.T) {
	if string(RenderPlist(testSpec())) != string(RenderPlist(testSpec())) {
		t.Fatal("two renders differ")
	}
}

func TestRenderPlistWithoutOptionalParts(t *testing.T) {
	got := string(RenderPlist(Spec{Label: "x", Program: []string{"/bin/true"}}))
	for _, unwanted := range []string{"EnvironmentVariables", "StandardOutPath"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("plist should not have %s:\n%s", unwanted, got)
		}
	}
}

// TestRenderPlistIsValid asks macOS's own tool, where it exists.
func TestRenderPlistIsValid(t *testing.T) {
	plutil, err := exec.LookPath("plutil")
	if err != nil {
		t.Skip("plutil is not available")
	}
	path := filepath.Join(t.TempDir(), "test.plist")
	if err := os.WriteFile(path, RenderPlist(testSpec()), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(plutil, "-lint", path).CombinedOutput(); err != nil {
		t.Fatalf("plutil: %v\n%s", err, out)
	}
}

func TestPlistPath(t *testing.T) {
	t.Setenv("HOME", "/Users/me")
	got, err := PlistPath(Label)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/Users/me/Library/LaunchAgents/io.github.tornikegomareli.rexd.plist" {
		t.Fatalf("got %q", got)
	}
}

// launchctl cannot run under an ended context, and that must not read as "not loaded".
func TestStopReportsAnEndedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Stop(ctx, "io.github.tornikegomareli.rexd.test.none"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
