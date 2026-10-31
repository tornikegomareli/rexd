package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tornikegomareli/rexd/internal/paths"
	"github.com/tornikegomareli/rexd/internal/state"
	"github.com/tornikegomareli/rexd/internal/testutil/fakerex"
)

func TestMain(m *testing.M) {
	os.Exit(fakerex.RunTests(m))
}

type cliEnv struct {
	t    *testing.T
	fake *fakerex.Fake
	dirs paths.Dirs
	init string
}

// newCLI points every path rexd uses at a temporary folder and at a fake rex.
func newCLI(t *testing.T) *cliEnv {
	t.Helper()
	root := t.TempDir()
	fake := fakerex.New(t)
	t.Setenv("REXD_REX", fake.Path)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	dirs, err := paths.Default()
	if err != nil {
		t.Fatal(err)
	}
	return &cliEnv{t: t, fake: fake, dirs: dirs, init: filepath.Join(root, "config", "rex", "init.lua")}
}

// run calls the command line with a LaunchAgent label that cannot exist.
func (e *cliEnv) run(args ...string) (int, string, string) {
	e.t.Helper()
	var out, errOut bytes.Buffer
	code := Main(args, Options{Version: "test", Stdout: &out, Stderr: &errOut, ServiceLabel: "io.github.tornikegomareli.rexd.test.none"})
	return code, out.String(), errOut.String()
}

func (e *cliEnv) mustRun(args ...string) string {
	e.t.Helper()
	code, out, errOut := e.run(args...)
	if code != 0 {
		e.t.Fatalf("rexd %v: exit %d\nstdout: %s\nstderr: %s", args, code, out, errOut)
	}
	return out
}

func (e *cliEnv) setup() {
	e.t.Helper()
	e.mustRun("setup", "--rex-config", e.init, "--no-service")
}

func samplePlugin(t *testing.T, id, version string) string {
	t.Helper()
	dir := t.TempDir()
	manifest := fmt.Sprintf(`id = %q
name = "Sample"
version = %q
config = "config.lua"

[[scripts]]
name = "one"
path = "one.lua"

[[scripts]]
name = "two"
path = "two.lua"
`, id, version)
	for name, content := range map[string]string{
		"rex-plugin.toml": manifest,
		"config.lua":      "-- config\n",
		"one.lua":         "-- one\n",
		"two.lua":         "-- two\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSetupInstallListRemove(t *testing.T) {
	e := newCLI(t)
	out := e.mustRun("setup", "--rex-config", e.init, "--no-service")
	if !strings.Contains(out, "Connected to "+e.init) || !strings.Contains(out, `Run "rexd daemon"`) {
		t.Errorf("setup output:\n%s", out)
	}

	if out := e.mustRun("list"); !strings.Contains(out, "No plugins are installed") {
		t.Errorf("empty list:\n%s", out)
	}

	src := samplePlugin(t, "acme.sample", "1.2.3")
	if out := e.mustRun("install", src); !strings.Contains(out, "Installed acme.sample 1.2.3") {
		t.Errorf("install output:\n%s", out)
	}

	out = e.mustRun("list")
	for _, want := range []string{"PLUGIN", "acme.sample", "1.2.3", "copy", src} {
		if !strings.Contains(out, want) {
			t.Errorf("list has no %q:\n%s", want, out)
		}
	}

	var listed []state.Plugin
	if err := json.Unmarshal([]byte(e.mustRun("list", "--json")), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != "acme.sample" {
		t.Errorf("list --json = %+v", listed)
	}

	if out := e.mustRun("remove", "acme.sample"); !strings.Contains(out, "Removed acme.sample") {
		t.Errorf("remove output:\n%s", out)
	}
	if out := e.mustRun("list", "--json"); strings.TrimSpace(out) != "[]" {
		t.Errorf("list --json after remove: %s", out)
	}
}

func TestFlagsMayFollowArguments(t *testing.T) {
	e := newCLI(t)
	e.setup()
	src := samplePlugin(t, "acme.sample", "1.0")
	e.mustRun("install", src, "--link")
	info, err := os.Lstat(e.dirs.Plugin("acme.sample"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("--link after the source was ignored: %v, %v", info, err)
	}
}

func TestUsageErrorsExitWithStatus2(t *testing.T) {
	e := newCLI(t)
	for _, args := range [][]string{
		{"install"},
		{"install", "a", "b"},
		{"install", "not-a-source"},
		{"install", "--no-such-flag", "./x"},
		{"remove"},
		{"remove", "a", "b"},
		{"setup", "extra"},
		{"list", "extra"},
		{"status", "extra"},
		{"teardown", "extra"},
		{"teardown", "--no-service"},
		{"daemon", "extra"},
		{"logs"},
		{"logs", "--daemon", "a.b"},
		{"logs", "a.b", "-n", "-1"},
	} {
		code, _, errOut := e.run(args...)
		if code != 2 {
			t.Errorf("rexd %v: exit %d, stderr %q", args, code, errOut)
		}
		if !strings.Contains(errOut, "Usage: rexd "+args[0]) {
			t.Errorf("rexd %v: no usage line in %q", args, errOut)
		}
	}
}

func TestFailuresExitWithStatus1(t *testing.T) {
	e := newCLI(t)
	src := samplePlugin(t, "acme.sample", "1.0")
	code, _, errOut := e.run("install", src)
	if code != 1 || !strings.Contains(errOut, `run "rexd setup" first`) {
		t.Fatalf("install before setup: exit %d, stderr %q", code, errOut)
	}
	e.setup()
	code, _, errOut = e.run("remove", "acme.nope")
	if code != 1 || !strings.Contains(errOut, "not installed") {
		t.Fatalf("remove unknown: exit %d, stderr %q", code, errOut)
	}
}

func TestMissingRex(t *testing.T) {
	e := newCLI(t)
	t.Setenv("REXD_REX", "")
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	code, _, errOut := e.run("setup")
	if code != 1 || !strings.Contains(errOut, "could not find the rex command") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
}

func TestUpdateOutput(t *testing.T) {
	e := newCLI(t)
	e.setup()
	if out := e.mustRun("update"); !strings.Contains(out, "No plugins are installed") {
		t.Errorf("update with nothing:\n%s", out)
	}
	src := samplePlugin(t, "acme.sample", "1.0")
	e.mustRun("install", src)
	manifest := filepath.Join(src, "rex-plugin.toml")
	data, _ := os.ReadFile(manifest)
	os.WriteFile(manifest, bytes.Replace(data, []byte(`"1.0"`), []byte(`"2.0"`), 1), 0o644)
	if out := e.mustRun("update", "acme.sample"); !strings.Contains(out, "Updated acme.sample: 1.0 -> 2.0") {
		t.Errorf("update output:\n%s", out)
	}
}

func TestStatusWithoutADaemon(t *testing.T) {
	e := newCLI(t)
	e.setup()
	if out := e.mustRun("status"); !strings.Contains(out, "Daemon: not running") || !strings.Contains(out, "No plugins") {
		t.Errorf("status:\n%s", out)
	}
	e.mustRun("install", samplePlugin(t, "acme.sample", "1.0"))
	out := e.mustRun("status")
	for _, want := range []string{"PLUGIN", "acme.sample", "loaded"} {
		if !strings.Contains(out, want) {
			t.Errorf("status has no %q:\n%s", want, out)
		}
	}

	var report statusReport
	if err := json.Unmarshal([]byte(e.mustRun("status", "--json")), &report); err != nil {
		t.Fatal(err)
	}
	if report.Daemon.Running || len(report.Plugins) != 1 || !report.Plugins[0].HasConfig || report.Plugins[0].Problem != "" {
		t.Errorf("report = %+v", report)
	}
}

func TestStatusReportsAPluginThatBrokeLater(t *testing.T) {
	e := newCLI(t)
	e.setup()
	src := samplePlugin(t, "acme.sample", "1.0")
	e.mustRun("install", src, "--link")
	os.WriteFile(filepath.Join(src, "config.lua"), []byte("BROKEN"), 0o644)
	if out := e.mustRun("status"); !strings.Contains(out, "acme.sample: config code:") {
		t.Errorf("status:\n%s", out)
	}
}

func TestWithoutVersionedRex(t *testing.T) {
	rexTerminal := "/Users/me/Library/Application Support/com.superlogical.rex/RexServer/Versions/1642/Rex Server.app/Contents/MacOS"
	got := withoutVersionedRex("/opt/homebrew/bin:" + rexTerminal + ":/usr/bin")
	if got != "/opt/homebrew/bin:/usr/bin" {
		t.Fatalf("got %q", got)
	}
	if got := withoutVersionedRex("/usr/bin:/bin"); got != "/usr/bin:/bin" {
		t.Fatalf("got %q", got)
	}
}

func TestDescribeScript(t *testing.T) {
	next := time.Now().Add(4 * time.Second)
	tests := []struct {
		script state.ScriptStatus
		rex    state.RexState
		want   string
	}{
		{state.ScriptStatus{State: state.ScriptRunning, PID: 42}, state.RexUp, "running, pid 42"},
		{state.ScriptStatus{State: state.ScriptWaiting}, state.RexDown, "waiting for Rex"},
		{state.ScriptStatus{State: state.ScriptWaiting}, state.RexNoSessions, "waiting for a session"},
		{state.ScriptStatus{State: state.ScriptWaiting}, state.RexUp, "starting"},
		{state.ScriptStatus{State: state.ScriptStopping}, state.RexUp, "stopping"},
		{state.ScriptStatus{State: state.ScriptBackoff, NextStart: next, LastExit: &state.Exit{Code: 1}}, state.RexUp, "exited with status 1, restarting in 4s"},
		{state.ScriptStatus{State: state.ScriptCrashing, NextStart: next, LastExit: &state.Exit{Code: 2}}, state.RexUp, "crashing with status 2, restarting in 4s"},
		{state.ScriptStatus{State: state.ScriptBackoff, NextStart: next, LastExit: &state.Exit{Code: -1, Error: "signal: killed"}}, state.RexUp, "exited: signal: killed, restarting in 4s"},
		{state.ScriptStatus{State: state.ScriptCrashing, NextStart: next, LastExit: &state.Exit{Code: -1, Error: "signal: killed"}}, state.RexUp, "crashing: signal: killed, restarting in 4s"},
	}
	for _, tt := range tests {
		if got := describeScript(tt.script, tt.rex); got != tt.want {
			t.Errorf("describeScript(%+v) = %q, want %q", tt.script, got, tt.want)
		}
	}
}

func TestLogs(t *testing.T) {
	e := newCLI(t)
	e.setup()
	e.mustRun("install", samplePlugin(t, "acme.sample", "1.0"))
	one := e.dirs.ScriptLog("acme.sample", "one")
	two := e.dirs.ScriptLog("acme.sample", "two")
	os.MkdirAll(filepath.Dir(one), 0o755)
	os.WriteFile(one, []byte("first\nsecond\nthird\n"), 0o644)
	os.WriteFile(two, []byte("other\n"), 0o644)

	if out := e.mustRun("logs", "acme.sample", "--script", "one", "-n", "2"); out != "second\nthird\n" {
		t.Errorf("one script: %q", out)
	}
	out := e.mustRun("logs", "acme.sample")
	for _, want := range []string{"==> " + one + " <==", "first", "==> " + two + " <==", "other"} {
		if !strings.Contains(out, want) {
			t.Errorf("all scripts have no %q:\n%s", want, out)
		}
	}

	code, _, errOut := e.run("logs", "acme.sample", "-f")
	if code != 2 || !strings.Contains(errOut, "-f follows one script") {
		t.Errorf("-f with two scripts: exit %d, %q", code, errOut)
	}
	code, _, errOut = e.run("logs", "acme.sample", "--script", "nope")
	if code != 1 || !strings.Contains(errOut, `no script named "nope"`) {
		t.Errorf("unknown script: exit %d, %q", code, errOut)
	}
	code, _, errOut = e.run("logs", "acme.nope")
	if code != 1 || !strings.Contains(errOut, "not installed") {
		t.Errorf("unknown plugin: exit %d, %q", code, errOut)
	}
}

func TestLogsReadTheRotatedFileToo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	os.WriteFile(path+".1", []byte("old one\nold two\n"), 0o644)
	os.WriteFile(path, []byte("new\n"), 0o644)
	var out bytes.Buffer
	if err := printTail(&out, path, 2); err != nil {
		t.Fatal(err)
	}
	if out.String() != "old two\nnew\n" {
		t.Fatalf("got %q", out.String())
	}
}

func TestLogsOfAMissingFilePrintNothing(t *testing.T) {
	var out bytes.Buffer
	if err := printTail(&out, filepath.Join(t.TempDir(), "x.log"), 10); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("got %q", out.String())
	}
}

func TestDaemonLogs(t *testing.T) {
	e := newCLI(t)
	os.MkdirAll(e.dirs.Logs(), 0o755)
	os.WriteFile(e.dirs.DaemonLog(), []byte("daemon line\n"), 0o644)
	if out := e.mustRun("logs", "--daemon"); out != "daemon line\n" {
		t.Errorf("got %q", out)
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestFollowFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	os.WriteFile(path, []byte("before\n"), 0o644)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- followFile(ctx, out, path) }()

	waitOutput := func(want string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !strings.Contains(out.String(), want) {
			if time.Now().After(deadline) {
				t.Fatalf("no %q in %q", want, out.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Give it time to take its first offset, so "before" is not reprinted.
	time.Sleep(50 * time.Millisecond)
	appendTo(t, path, "added\n")
	waitOutput("added\n")
	if strings.Contains(out.String(), "before") {
		t.Errorf("printed old lines: %q", out.String())
	}

	// A rotation leaves a shorter file, which it must read from the top.
	os.WriteFile(path, []byte("x\n"), 0o644)
	waitOutput("x\n")

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("did not stop")
	}
}

func appendTo(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.WriteString(content)
}

func TestTeardown(t *testing.T) {
	e := newCLI(t)
	e.setup()
	e.mustRun("install", samplePlugin(t, "acme.sample", "1.0"))
	out := e.mustRun("teardown")
	if !strings.Contains(out, "Plugins stay in "+e.dirs.Plugins()) {
		t.Errorf("output:\n%s", out)
	}
	if data, err := os.ReadFile(e.init); err == nil && strings.Contains(string(data), "rexd") {
		t.Errorf("init.lua still loads rexd: %q", data)
	}
	if out := e.mustRun("list"); !strings.Contains(out, "acme.sample") {
		t.Errorf("the plugin should stay installed:\n%s", out)
	}

	out = e.mustRun("teardown", "--purge")
	if !strings.Contains(out, "deleted") {
		t.Errorf("output:\n%s", out)
	}
	if out := e.mustRun("list"); !strings.Contains(out, "No plugins") {
		t.Errorf("purge should delete plugins:\n%s", out)
	}
}

func TestHelpForEveryCommand(t *testing.T) {
	e := newCLI(t)
	for _, name := range []string{"setup", "install", "remove", "update", "list", "status", "logs", "teardown", "daemon", "version"} {
		code, out, errOut := e.run("help", name)
		if code != 0 || !strings.Contains(out, "Usage: rexd "+name) {
			t.Errorf("help %s: exit %d\nstdout: %s\nstderr: %s", name, code, out, errOut)
		}
		code, out, _ = e.run(name, "--help")
		if code != 0 || !strings.Contains(out, "Usage: rexd "+name) {
			t.Errorf("%s --help: exit %d\n%s", name, code, out)
		}
	}
	_, out, _ := e.run("help", "install")
	for _, want := range []string{"owner/repo", "  --link\n", "  --ref REF\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("install help has no %q:\n%s", want, out)
		}
	}
}
