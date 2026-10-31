package rex_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tornikegomareli/rexd/internal/rex"
	"github.com/tornikegomareli/rexd/internal/testutil/fakerex"
)

func TestMain(m *testing.M) {
	fakerex.Main()
	os.Exit(m.Run())
}

func client(f *fakerex.Fake) rex.Client {
	return rex.Client{Path: f.Path, Env: f.Env}
}

func TestVersion(t *testing.T) {
	f := fakerex.New(t)
	f.SetAPI(9)
	v, err := client(f).Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.APIRevision != 9 || v.Rex != "fake" {
		t.Fatalf("got %+v", v)
	}
}

func TestVersionWorksWithTheServerDown(t *testing.T) {
	f := fakerex.New(t)
	f.SetDown(true)
	if _, err := client(f).Version(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSessions(t *testing.T) {
	f := fakerex.New(t)
	f.SetSessions("session:a", "session:b")
	got, err := client(f).Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"session:a", "session:b"}) {
		t.Fatalf("got %v", got)
	}

	f.SetSessions()
	got, err = client(f).Sessions(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("no sessions: %v, %v", got, err)
	}

	f.SetDown(true)
	if _, err := client(f).Sessions(context.Background()); !errors.Is(err, rex.ErrNotRunning) {
		t.Fatalf("server down: %v", err)
	}
}

func TestConfigPath(t *testing.T) {
	f := fakerex.New(t)
	f.SetConfigPath("/some/init.lua")
	got, err := client(f).ConfigPath(context.Background())
	if err != nil || got != "/some/init.lua" {
		t.Fatalf("got %q, %v", got, err)
	}
	f.SetDown(true)
	if _, err := client(f).ConfigPath(context.Background()); !errors.Is(err, rex.ErrNotRunning) {
		t.Fatalf("server down: %v", err)
	}
}

func TestCheckConfig(t *testing.T) {
	f := fakerex.New(t)
	dir := t.TempDir()
	good := filepath.Join(dir, "good.lua")
	bad := filepath.Join(dir, "bad.lua")
	os.WriteFile(good, []byte("-- fine\n"), 0o644)
	os.WriteFile(bad, []byte("BROKEN\n"), 0o644)

	f.SetDown(true) // Checking must not need a server.
	if err := client(f).CheckConfig(context.Background(), good); err != nil {
		t.Fatal(err)
	}
	err := client(f).CheckConfig(context.Background(), bad)
	var configErr *rex.ConfigError
	if !errors.As(err, &configErr) {
		t.Fatalf("got %v, want a ConfigError", err)
	}
	if got := configErr.Error(); got != "bad.lua:1: broken" {
		t.Fatalf("message = %q", got)
	}
}

func TestReloadConfig(t *testing.T) {
	f := fakerex.New(t)
	if err := client(f).ReloadConfig(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.Reloads() != 1 {
		t.Fatalf("reloads = %d", f.Reloads())
	}
	f.SetDown(true)
	if err := client(f).ReloadConfig(context.Background()); !errors.Is(err, rex.ErrNotRunning) {
		t.Fatalf("server down: %v", err)
	}
}

func TestEveryCallDisablesAutostartAndPassesTheServer(t *testing.T) {
	f := fakerex.New(t)
	c := client(f)
	c.Server = "unix:///test.sock"
	if _, err := c.Sessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %v", calls)
	}
	want := []string{"--autostart=false", "-S", "unix:///test.sock", "ls", "-q", "--short=false"}
	if !slices.Equal(calls[0], want) {
		t.Fatalf("call = %v, want %v", calls[0], want)
	}
}

func TestCommandsDoNotInheritRexTerminalVariables(t *testing.T) {
	t.Setenv("REX_SERVER", "unix:///wrong.sock")
	t.Setenv("REX_SESSION", "session:wrong")
	t.Setenv("REX_BLOCK", "block:wrong")
	t.Setenv("REX_CLIENT", "client:wrong")
	t.Setenv("REX_BIN_DIR", "/kept")
	env := client(fakerex.New(t)).Command(context.Background(), "ls").Env
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "REX_SERVER", "REX_SESSION", "REX_BLOCK", "REX_CLIENT":
			t.Errorf("inherited %s", kv)
		}
	}
	if !slices.Contains(env, "REX_BIN_DIR=/kept") {
		t.Error("removed a variable it should keep")
	}
}

func TestScriptCommand(t *testing.T) {
	t.Setenv("REX_SESSION", "session:wrong")
	c := rex.Client{Path: "rex", Server: "unix:///s.sock"}
	wantArgs := []string{"--autostart=false", "-S", "unix:///s.sock", "do", "/p/notify.lua", "--all"}
	if got := c.ScriptArgs("/p/notify.lua"); !slices.Equal(got, wantArgs) {
		t.Fatalf("args = %v", got)
	}
	env := c.ScriptEnv("session:right")
	if !slices.Contains(env, "REX_SESSION=session:right") || slices.Contains(env, "REX_SESSION=session:wrong") {
		t.Fatalf("env has the wrong session: %v", env)
	}
}

func TestFindPrefersTheOverride(t *testing.T) {
	t.Setenv("REXD_REX", "/custom/rex")
	got, err := rex.Find()
	if err != nil || got != "/custom/rex" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestFindUsesThePath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "rex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REXD_REX", "")
	t.Setenv("PATH", dir)
	got, err := rex.Find()
	if err != nil || got != bin {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestLocalConfigPath(t *testing.T) {
	t.Setenv("REX_CONFIG", "/explicit/init.lua")
	if got, _ := rex.LocalConfigPath(); got != "/explicit/init.lua" {
		t.Errorf("REX_CONFIG: got %q", got)
	}
	t.Setenv("REX_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if got, _ := rex.LocalConfigPath(); got != "/xdg/rex/init.lua" {
		t.Errorf("XDG: got %q", got)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	home, _ := os.UserHomeDir()
	if got, _ := rex.LocalConfigPath(); got != filepath.Join(home, ".config/rex/init.lua") {
		t.Errorf("default: got %q", got)
	}
}
