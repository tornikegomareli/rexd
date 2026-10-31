//go:build e2e

// Package e2e tests rexd against a real Rex server. Each test starts its own
// server with its own socket, state and config in a temporary folder, and checks
// the server's config path before doing anything else. No test can touch the
// Rex server you work in. Run them with: make e2e
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tornikegomareli/rexd/internal/cli"
	"github.com/tornikegomareli/rexd/internal/rex"
	"github.com/tornikegomareli/rexd/internal/service"
	"github.com/tornikegomareli/rexd/internal/state"
)

const waitFor = 30 * time.Second

// rexdBinary is built once, in TestMain.
var rexdBinary string

func TestMain(m *testing.M) {
	os.Exit(func() int {
		dir, err := os.MkdirTemp("", "rexd-e2e-bin")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer os.RemoveAll(dir)
		rexdBinary = filepath.Join(dir, "rexd")
		args := []string{"build", "-o", rexdBinary}
		// A race-enabled rexd finds races in the real daemon, but starts slowly.
		if os.Getenv("REXD_E2E_RACE") != "" {
			args = append(args, "-race")
		}
		if out, err := exec.Command("go", append(args, "../cmd/rexd")...).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "building rexd: %v\n%s", err, out)
			return 1
		}
		return m.Run()
	}())
}

// rig is one isolated Rex server and one isolated rexd.
type rig struct {
	t         *testing.T
	root      string
	rexPath   string
	server    string
	init      string
	marker    string
	env       []string
	serverCmd *exec.Cmd
	daemonCmd *exec.Cmd
}

func newRig(t *testing.T) *rig {
	t.Helper()
	rexPath, err := rex.Find()
	if err != nil {
		t.Skipf("Rex is not installed: %v", err)
	}
	// Not t.TempDir: its long paths do not fit in a Unix socket address.
	root, err := os.MkdirTemp("", "rexd-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{
		t:       t,
		root:    root,
		rexPath: rexPath,
		server:  "unix://" + filepath.Join(root, "rex.sock"),
		init:    filepath.Join(root, "config", "rex", "init.lua"),
		marker:  filepath.Join(root, "state", "e2e-marker.txt"),
	}
	if err := os.MkdirAll(filepath.Dir(r.init), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, kv := range os.Environ() {
		// A Rex terminal sets these, and they would point at the real server.
		if !strings.HasPrefix(kv, "REX_") {
			r.env = append(r.env, kv)
		}
	}
	r.env = append(r.env,
		"REXD_REX="+rexPath,
		"XDG_DATA_HOME="+filepath.Join(root, "data"),
		"XDG_STATE_HOME="+filepath.Join(root, "state"),
	)
	t.Cleanup(func() {
		r.stopDaemon()
		r.stopServer()
		if !t.Failed() {
			os.RemoveAll(root)
		} else {
			t.Logf("kept %s for inspection", root)
		}
	})
	r.startServer()
	return r
}

// startServer starts the isolated server and proves it is the one tests talk to.
func (r *rig) startServer() {
	r.t.Helper()
	logFile, err := os.OpenFile(filepath.Join(r.root, "server.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		r.t.Fatal(err)
	}
	defer logFile.Close()
	cmd := exec.Command(r.rexPath, "server", "run",
		"--listen", r.server,
		"--state-dir", filepath.Join(r.root, "rex-state"),
		"--config", r.init,
		"--restore=false",
	)
	cmd.Env = r.env
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		r.t.Fatal(err)
	}
	r.serverCmd = cmd
	r.eventually("the Rex server to answer", func() bool {
		_, err := r.rexOutput("ls")
		return err == nil
	})
	var keymap struct {
		Config struct {
			Path string `json:"path"`
		} `json:"config"`
	}
	r.rexJSON(&keymap, "keymap", "--json")
	if keymap.Config.Path != r.init {
		r.t.Fatalf("talking to the wrong server: its config is %q, want %q", keymap.Config.Path, r.init)
	}
}

func (r *rig) stopServer() {
	if r.serverCmd == nil {
		return
	}
	r.serverCmd.Process.Signal(syscall.SIGTERM)
	exited := make(chan struct{})
	go func() { r.serverCmd.Wait(); close(exited) }()
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		r.serverCmd.Process.Kill()
		<-exited
	}
	r.serverCmd = nil
}

func (r *rig) eventually(what string, cond func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	r.t.Fatalf("timed out waiting for: %s\nmarker:\n%s", what, r.markerText())
}

func (r *rig) markerText() string {
	data, _ := os.ReadFile(r.marker)
	return string(data)
}

func (r *rig) waitMarker(line string) {
	r.t.Helper()
	r.eventually("marker line "+line, func() bool { return r.markerCount(line) > 0 })
}

func (r *rig) markerCount(line string) int {
	n := 0
	for got := range strings.Lines(r.markerText()) {
		if strings.TrimSpace(got) == line {
			n++
		}
	}
	return n
}

// rexOutput runs rex against the isolated server. -S fails if the server is
// missing, while REX_SERVER would silently fall back to the real one.
func (r *rig) rexOutput(args ...string) (string, error) {
	cmd := exec.Command(r.rexPath, append([]string{"--autostart=false", "-S", r.server}, args...)...)
	cmd.Env = r.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (r *rig) rex(args ...string) string {
	r.t.Helper()
	out, err := r.rexOutput(args...)
	if err != nil {
		r.t.Fatalf("rex %v: %v\n%s", args, err, out)
	}
	return out
}

func (r *rig) rexJSON(v any, args ...string) {
	r.t.Helper()
	if err := json.Unmarshal([]byte(r.rex(args...)), v); err != nil {
		r.t.Fatalf("rex %v: %v", args, err)
	}
}

func (r *rig) newSession(label string) {
	r.t.Helper()
	r.rex("new", label, "-c", r.root, "--json")
}

// runAction runs a config action in a session and returns what it printed.
func (r *rig) runAction(action, session string) (string, error) {
	return r.rexOutput("do", action, "-s", session)
}

// rexd runs the rexd command and returns its output and exit status.
func (r *rig) rexd(args ...string) (string, int) {
	r.t.Helper()
	cmd := exec.Command(rexdBinary, args...)
	cmd.Env = r.env
	cmd.Dir = r.root
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err == nil {
		return out.String(), 0
	}
	exit, ok := err.(*exec.ExitError)
	if !ok {
		r.t.Fatalf("rexd %v: %v", args, err)
	}
	return out.String(), exit.ExitCode()
}

func (r *rig) mustRexd(args ...string) string {
	r.t.Helper()
	out, code := r.rexd(args...)
	if code != 0 {
		r.t.Fatalf("rexd %v: exit %d\n%s", args, code, out)
	}
	return out
}

func (r *rig) setup() {
	r.t.Helper()
	r.mustRexd("setup", "--server", r.server, "--rex-config", r.init, "--no-service")
}

// cliInProcess runs the command line inside the test, with a LaunchAgent label of
// its own. Commands that touch the LaunchAgent must run this way: the rexd binary
// would use the normal label, and so a real installation.
func (r *rig) cliInProcess(label string, args ...string) string {
	r.t.Helper()
	for _, kv := range r.env {
		name, value, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "XDG_") || strings.HasPrefix(name, "REXD_") {
			r.t.Setenv(name, value)
		}
	}
	var out bytes.Buffer
	code := cli.Main(args, cli.Options{Stdout: &out, Stderr: &out, ServiceLabel: label, Executable: rexdBinary})
	if code != 0 {
		r.t.Fatalf("rexd %v: exit %d\n%s", args, code, out.String())
	}
	return out.String()
}

func (r *rig) startDaemon() {
	r.t.Helper()
	logFile, err := os.OpenFile(filepath.Join(r.root, "daemon.out"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		r.t.Fatal(err)
	}
	defer logFile.Close()
	cmd := exec.Command(rexdBinary, "daemon")
	cmd.Env = r.env
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		r.t.Fatal(err)
	}
	r.daemonCmd = cmd
	r.eventually("the daemon to run", func() bool { return r.status().Daemon.Running })
}

func (r *rig) stopDaemon() {
	if r.daemonCmd == nil {
		return
	}
	r.daemonCmd.Process.Signal(syscall.SIGTERM)
	exited := make(chan error, 1)
	go func() { exited <- r.daemonCmd.Wait() }()
	select {
	case err := <-exited:
		// A daemon told to stop exits cleanly. A race in it would show up here too.
		if err != nil {
			output, _ := os.ReadFile(filepath.Join(r.root, "daemon.out"))
			r.t.Errorf("the daemon exited with %v\n%s", err, output)
		}
	case <-time.After(15 * time.Second):
		r.t.Error("the daemon did not stop on SIGTERM")
		r.daemonCmd.Process.Kill()
		<-exited
	}
	r.daemonCmd = nil
}

type report struct {
	Daemon struct {
		Running bool `json:"running"`
		PID     int  `json:"pid"`
	} `json:"daemon"`
	Rex     state.RexState `json:"rex"`
	Plugins []struct {
		ID        string               `json:"id"`
		Version   string               `json:"version"`
		HasConfig bool                 `json:"has_config"`
		Problem   string               `json:"problem"`
		Scripts   []state.ScriptStatus `json:"scripts"`
	} `json:"plugins"`
}

func (r *rig) status() report {
	r.t.Helper()
	var got report
	if err := json.Unmarshal([]byte(r.mustRexd("status", "--json")), &got); err != nil {
		r.t.Fatal(err)
	}
	return got
}

func (r *rig) script(plugin string) state.ScriptStatus {
	r.t.Helper()
	for _, p := range r.status().Plugins {
		if p.ID == plugin && len(p.Scripts) > 0 {
			return p.Scripts[0]
		}
	}
	return state.ScriptStatus{}
}

func (r *rig) waitRunning(plugin string, notPID int) state.ScriptStatus {
	r.t.Helper()
	var got state.ScriptStatus
	r.eventually(plugin+" to run", func() bool {
		got = r.script(plugin)
		return got.State == state.ScriptRunning && got.PID != notPID
	})
	return got
}

const probeID = "rexd-e2e.probe-plugin"

// plugin writes a plugin that has an action and an event script, and returns its
// folder. The action reports its version. The script writes to the marker file.
func (r *rig) plugin(id, action, version string) string {
	r.t.Helper()
	dir, err := os.MkdirTemp(r.root, "src-")
	if err != nil {
		r.t.Fatal(err)
	}
	r.writePlugin(dir, id, action, version)
	return dir
}

func (r *rig) writePlugin(dir, id, action, version string) {
	r.t.Helper()
	files := map[string]string{
		"rex-plugin.toml": fmt.Sprintf(`id = %q
name = "E2E probe"
version = %q
rex_api = 1
config = "rex/config.lua"

[[scripts]]
name = "probe"
path = "rex/probe.lua"
`, id, version),
		"rex/config.lua": fmt.Sprintf(`local plugin = rexd.plugin
rex.action{
  name = %q,
  title = "E2E probe",
  run = function() return plugin.id .. " " .. plugin.version end,
}
rex.bind("ctrl+alt+shift+f12", %q)
`, action, action),
		// A LaunchAgent only passes on a few variables, and this is one of them.
		"rex/probe.lua": `local marker = os.getenv("XDG_STATE_HOME") .. "/e2e-marker.txt"
local function record(line)
  local file = io.open(marker, "a")
  file:write(line .. "\n")
  file:close()
end
record("started " .. os.getenv("REXD_PLUGIN_ID") .. " " .. os.getenv("REXD_PLUGIN_VERSION"))
rex.on("session_created", function(ctx, ev)
  record("session " .. ev.session.label)
end)
`,
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
}

func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func TestInstallRunAndRemove(t *testing.T) {
	r := newRig(t)
	r.setup()
	r.newSession("first")
	r.newSession("second") // Two sessions and no app make rex's default target ambiguous.
	r.startDaemon()
	src := r.plugin(probeID, "e2e_probe", "1.0")

	if out := r.mustRexd("install", src, "--link"); !strings.Contains(out, "Installed "+probeID+" 1.0") {
		t.Fatalf("install output:\n%s", out)
	}

	// The config code runs inside Rex and knows its plugin.
	out, err := r.runAction("e2e_probe", "first")
	if err != nil || !strings.Contains(out, probeID+" 1.0") {
		t.Fatalf("action: %v\n%s", err, out)
	}

	// The event script runs, with its environment.
	first := r.waitRunning(probeID, 0)
	r.waitMarker("started " + probeID + " 1.0")
	if r.status().Rex != state.RexUp {
		t.Errorf("rex state = %q", r.status().Rex)
	}

	// It hears a session created now.
	r.newSession("third")
	r.waitMarker("session third")

	// A killed script comes back, and only one copy handles each event.
	if err := syscall.Kill(first.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	second := r.waitRunning(probeID, first.PID)
	if second.Restarts != 1 {
		t.Errorf("restarts = %d, want 1", second.Restarts)
	}
	if second.LastExit == nil || second.LastExit.Code != -1 || second.LastExit.Error != "signal: killed" {
		t.Errorf("last exit = %+v", second.LastExit)
	}
	r.eventually("the second start", func() bool { return r.markerCount("started "+probeID+" 1.0") == 2 })
	r.newSession("fourth")
	r.waitMarker("session fourth")
	time.Sleep(200 * time.Millisecond)
	if got := r.markerCount("session fourth"); got != 1 {
		t.Errorf("the event was handled %d times", got)
	}

	// The log shows the script's life.
	if logs := r.mustRexd("logs", probeID); !strings.Contains(logs, "rexd: started") {
		t.Errorf("logs:\n%s", logs)
	}

	// Remove takes everything away except the linked source.
	r.mustRexd("remove", probeID)
	r.eventually("the script to stop", func() bool { return !alive(second.PID) })
	if out, err := r.runAction("e2e_probe", "first"); err == nil {
		t.Errorf("the action should be gone:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(src, "rex/config.lua")); err != nil {
		t.Errorf("remove harmed the source: %v", err)
	}
	if out := r.mustRexd("list"); strings.Contains(out, probeID) {
		t.Errorf("list:\n%s", out)
	}
}

func TestStatusNamesRexOnlyWhileAScriptWatchesIt(t *testing.T) {
	r := newRig(t)
	r.setup()
	r.newSession("first")
	r.startDaemon()

	if out := r.mustRexd("status"); strings.Contains(out, "Rex:") {
		t.Errorf("no script watches Rex, so the daemon cannot know:\n%s", out)
	}

	r.mustRexd("install", r.plugin(probeID, "e2e_probe", "1.0"), "--link")
	r.waitRunning(probeID, 0)
	if out := r.mustRexd("status"); !strings.Contains(out, "Rex:    running") {
		t.Errorf("status:\n%s", out)
	}

	// The daemon's last answer is old once nothing watches Rex.
	r.mustRexd("remove", probeID)
	r.eventually("status to stop naming Rex", func() bool {
		return !strings.Contains(r.mustRexd("status"), "Rex:")
	})
}

func TestCopyInstallAndPlainGitInstall(t *testing.T) {
	r := newRig(t)
	r.setup()
	r.newSession("first")

	src := r.plugin("rexd-e2e.copied", "e2e_copied", "1.0")
	r.mustRexd("install", src)
	if err := os.RemoveAll(src); err != nil {
		t.Fatal(err)
	}
	if out, err := r.runAction("e2e_copied", "first"); err != nil || !strings.Contains(out, "rexd-e2e.copied 1.0") {
		t.Fatalf("a copy must not need its source: %v\n%s", err, out)
	}

	repo := r.plugin("rexd-e2e.cloned", "e2e_cloned", "3.0")
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "first"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	r.mustRexd("install", "file://"+repo)
	if out, err := r.runAction("e2e_cloned", "first"); err != nil || !strings.Contains(out, "rexd-e2e.cloned 3.0") {
		t.Fatalf("git install: %v\n%s", err, out)
	}
}

// A daemon that dies leaves its scripts running. The next one must replace them,
// or every event gets handled twice.
func TestKilledDaemonLeavesNoDuplicates(t *testing.T) {
	r := newRig(t)
	r.setup()
	r.newSession("first")
	r.startDaemon()
	r.mustRexd("install", r.plugin(probeID, "e2e_probe", "1.0"), "--link")
	orphan := r.waitRunning(probeID, 0)
	r.waitMarker("started " + probeID + " 1.0")

	if err := r.daemonCmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	r.daemonCmd.Wait()
	r.daemonCmd = nil
	if !alive(orphan.PID) {
		t.Fatal("expected the script to outlive the killed daemon")
	}

	r.startDaemon()
	replacement := r.waitRunning(probeID, orphan.PID)
	r.eventually("the orphan to stop", func() bool { return !alive(orphan.PID) })
	r.eventually("the replacement to start", func() bool { return r.markerCount("started "+probeID+" 1.0") == 2 })

	r.newSession("after")
	r.waitMarker("session after")
	time.Sleep(200 * time.Millisecond)
	if got := r.markerCount("session after"); got != 1 {
		t.Fatalf("the event was handled %d times", got)
	}
	if replacement.Restarts != 0 {
		t.Errorf("restarts = %d", replacement.Restarts)
	}
}

// Rex going away is not a failure. Scripts wait, and come back when Rex does.
func TestRexRestart(t *testing.T) {
	r := newRig(t)
	r.setup()
	r.newSession("first")
	r.startDaemon()
	r.mustRexd("install", r.plugin(probeID, "e2e_probe", "1.0"), "--link")
	before := r.waitRunning(probeID, 0)
	r.waitMarker("started " + probeID + " 1.0")

	r.stopServer()
	r.eventually("the script to wait for Rex", func() bool {
		got := r.status()
		return got.Rex == state.RexDown && r.script(probeID).State == state.ScriptWaiting
	})
	if alive(before.PID) {
		t.Fatal("the script should have exited with the server")
	}
	if got := r.script(probeID); got.Restarts != 0 || got.LastExit != nil {
		t.Errorf("Rex stopping is not the script's failure: %+v", got)
	}
	if out := r.mustRexd("status"); !strings.Contains(out, "waiting for Rex") {
		t.Errorf("status:\n%s", out)
	}

	// With --restore=false, Rex comes back with no sessions.
	r.startServer()
	r.eventually("the script to wait for a session", func() bool { return r.status().Rex == state.RexNoSessions })
	if got := r.script(probeID); got.State != state.ScriptWaiting {
		t.Errorf("state = %q", got.State)
	}
	if out := r.mustRexd("status"); !strings.Contains(out, "waiting for a session") {
		t.Errorf("status:\n%s", out)
	}

	r.newSession("back")
	after := r.waitRunning(probeID, before.PID)
	if after.Restarts != 0 || after.LastExit != nil {
		t.Errorf("after Rex came back: %+v", after)
	}
	r.eventually("the script to start again", func() bool { return r.markerCount("started "+probeID+" 1.0") == 2 })
	r.newSession("heard")
	r.waitMarker("session heard")

	// The config survived too: Rex read it when it started.
	if out, err := r.runAction("e2e_probe", "back"); err != nil || !strings.Contains(out, probeID+" 1.0") {
		t.Errorf("action after restart: %v\n%s", err, out)
	}
}

func TestUpdate(t *testing.T) {
	r := newRig(t)
	r.setup()
	r.newSession("first")
	r.startDaemon()
	src := r.plugin(probeID, "e2e_probe", "1.0")
	r.mustRexd("install", src, "--link")
	first := r.waitRunning(probeID, 0)
	r.waitMarker("started " + probeID + " 1.0")

	r.writePlugin(src, probeID, "e2e_probe", "2.0")
	if out := r.mustRexd("update", probeID); !strings.Contains(out, "1.0 -> 2.0") {
		t.Fatalf("update output:\n%s", out)
	}

	// Rex really ran the new code. This also shows reload does not reuse a module
	// that an earlier load already required.
	if out, err := r.runAction("e2e_probe", "first"); err != nil || !strings.Contains(out, probeID+" 2.0") {
		t.Fatalf("action after update: %v\n%s", err, out)
	}
	// The script restarted with the new version, and that is not a crash.
	second := r.waitRunning(probeID, first.PID)
	r.waitMarker("started " + probeID + " 2.0")
	if second.Restarts != 0 {
		t.Errorf("restarts = %d", second.Restarts)
	}
	if alive(first.PID) {
		t.Error("the old script is still running")
	}
}

func TestBrokenUpdateKeepsTheOldVersion(t *testing.T) {
	r := newRig(t)
	r.setup()
	r.newSession("first")
	src := r.plugin(probeID, "e2e_probe", "1.0")
	r.mustRexd("install", src) // A copy, so the old version has its own files.

	r.writePlugin(src, probeID, "e2e_probe", "2.0")
	if err := os.WriteFile(filepath.Join(src, "rex/config.lua"), []byte("this is not lua ("), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := r.rexd("update", probeID)
	if code != 1 || !strings.Contains(out, "old version stays") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if got := r.status().Plugins[0].Version; got != "1.0" {
		t.Errorf("version = %q", got)
	}
	r.rex("config", "reload")
	if out, err := r.runAction("e2e_probe", "first"); err != nil || !strings.Contains(out, probeID+" 1.0") {
		t.Fatalf("action: %v\n%s", err, out)
	}
}

func TestBrokenInstallChangesNothing(t *testing.T) {
	r := newRig(t)
	r.setup()
	r.newSession("first")
	initBefore, _ := os.ReadFile(r.init)
	indexPath := filepath.Join(filepath.Dir(r.init), "rexd.lua")
	indexBefore, _ := os.ReadFile(indexPath)

	src := r.plugin(probeID, "e2e_probe", "1.0")
	if err := os.WriteFile(filepath.Join(src, "rex/config.lua"), []byte("this is not lua ("), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := r.rexd("install", src)
	if code != 1 || !strings.Contains(out, "did not install") || !strings.Contains(out, "parse error") {
		t.Fatalf("exit %d\n%s", code, out)
	}

	if got, _ := os.ReadFile(r.init); !bytes.Equal(got, initBefore) {
		t.Errorf("init.lua changed:\n%s", got)
	}
	if got, _ := os.ReadFile(indexPath); !bytes.Equal(got, indexBefore) {
		t.Errorf("rexd.lua changed:\n%s", got)
	}
	if out := r.mustRexd("list"); strings.Contains(out, probeID) {
		t.Errorf("list:\n%s", out)
	}
	r.assertConfigClean()
}

// assertConfigClean checks that Rex loads the config with no complaints.
func (r *rig) assertConfigClean() {
	r.t.Helper()
	r.rex("config", "reload")
	var keymap struct {
		Diagnostics []any `json:"diagnostics"`
	}
	r.rexJSON(&keymap, "keymap", "--json")
	if len(keymap.Diagnostics) != 0 {
		r.t.Errorf("Rex reports: %v", keymap.Diagnostics)
	}
}

// A plugin can still break after install, such as a linked one being edited. It
// must not take the user's config or other plugins down with it.
func TestBrokenPluginDoesNotAffectTheRest(t *testing.T) {
	r := newRig(t)
	r.setup()
	r.newSession("first")
	good := r.plugin("rexd-e2e.good", "e2e_good", "1.0")
	bad := r.plugin("rexd-e2e.bad", "e2e_bad", "1.0")
	r.mustRexd("install", good, "--link")
	r.mustRexd("install", bad, "--link")
	addUserConfig(t, r.init, `rex.action{ name = "e2e_mine", title = "Mine", run = function() return "user action" end }`+"\n")
	r.rex("config", "reload")

	if err := os.WriteFile(filepath.Join(bad, "rex/config.lua"), []byte("this is not lua ("), 0o644); err != nil {
		t.Fatal(err)
	}
	r.rex("config", "reload")

	if out, err := r.runAction("e2e_good", "first"); err != nil || !strings.Contains(out, "1.0") {
		t.Errorf("the other plugin stopped working: %v\n%s", err, out)
	}
	if out, err := r.runAction("e2e_mine", "first"); err != nil || !strings.Contains(out, "user action") {
		t.Errorf("the user's own action stopped working: %v\n%s", err, out)
	}
	if out, err := r.runAction("e2e_bad", "first"); err == nil {
		t.Errorf("the broken plugin's action should be gone:\n%s", out)
	}

	// rexd status says what is wrong, because Rex's log does not show it.
	if out := r.mustRexd("status"); !strings.Contains(out, "rexd-e2e.bad: config code:") || !strings.Contains(out, "parse error") {
		t.Errorf("status:\n%s", out)
	}
}

func addUserConfig(t *testing.T, path, content string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, content...), 0o644); err != nil {
		t.Fatal(err)
	}
}

// require("rexd") goes first in init.lua, so a key the user binds beats a plugin's.
func TestUserKeyBindingWins(t *testing.T) {
	r := newRig(t)
	r.setup()
	r.mustRexd("install", r.plugin(probeID, "e2e_probe", "1.0"))
	addUserConfig(t, r.init, `rex.bind("ctrl+alt+shift+f12", "pane.zoom")`+"\n")
	r.rex("config", "reload")

	var keymap struct {
		Bindings []struct {
			Keys   []string `json:"keys"`
			Action string   `json:"action"`
		} `json:"bindings"`
	}
	r.rexJSON(&keymap, "keymap", "--json")
	for _, b := range keymap.Bindings {
		if slices.Contains(b.Keys, "ctrl+alt+shift+f12") {
			if b.Action != "pane.zoom" {
				t.Errorf("top binding = %q, want the user's", b.Action)
			}
			return
		}
	}
	t.Fatalf("the key is not bound: %+v", keymap.Bindings)
}

func TestSetupAsksTheServerAndKeepsTheUsersConfig(t *testing.T) {
	r := newRig(t)
	if err := os.WriteFile(r.init, []byte(`rex.bind("cmd+e", "pane.zoom")`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.rex("config", "reload")
	// No --rex-config: the running server says where its config is.
	out := r.mustRexd("setup", "--server", r.server, "--no-service")
	if !strings.Contains(out, "Connected to "+r.init) {
		t.Fatalf("setup output:\n%s", out)
	}
	got, _ := os.ReadFile(r.init)
	if !strings.HasPrefix(string(got), `require("rexd")`) || !strings.Contains(string(got), `rex.bind("cmd+e", "pane.zoom")`) {
		t.Errorf("init.lua:\n%s", got)
	}
	r.assertConfigClean()
}

func TestSetupWithRexStopped(t *testing.T) {
	r := newRig(t)
	r.stopServer()
	out := r.mustRexd("setup", "--server", r.server, "--rex-config", r.init, "--no-service")
	if !strings.Contains(out, "Rex is not running") {
		t.Errorf("setup output:\n%s", out)
	}
	src := r.plugin(probeID, "e2e_probe", "1.0")
	if out := r.mustRexd("install", src); !strings.Contains(out, "Rex is not running") {
		t.Errorf("install output:\n%s", out)
	}

	// The strict check works without a server, and Rex loads the plugin at start.
	r.startServer()
	r.newSession("first")
	if out, err := r.runAction("e2e_probe", "first"); err != nil || !strings.Contains(out, "1.0") {
		t.Fatalf("action: %v\n%s", err, out)
	}
}

func TestTeardownAndSetupAgain(t *testing.T) {
	r := newRig(t)
	r.setup()
	addUserConfig(t, r.init, "-- my own line\n")
	r.newSession("first")
	r.startDaemon()
	r.mustRexd("install", r.plugin(probeID, "e2e_probe", "1.0"))
	script := r.waitRunning(probeID, 0)

	r.cliInProcess(service.Label+".e2e-none", "teardown")
	r.daemonCmd.Wait()
	r.daemonCmd = nil
	if alive(script.PID) {
		t.Error("the script outlived the daemon")
	}
	if got, _ := os.ReadFile(r.init); string(got) != "-- my own line\n" {
		t.Errorf("init.lua = %q", got)
	}
	r.assertConfigClean()
	if out, err := r.runAction("e2e_probe", "first"); err == nil {
		t.Errorf("the action should be gone:\n%s", out)
	}

	r.setup()
	if out, err := r.runAction("e2e_probe", "first"); err != nil || !strings.Contains(out, "1.0") {
		t.Fatalf("setup should bring the plugin back: %v\n%s", err, out)
	}
	r.assertConfigClean()
}

func TestPluginInAPathWithSpaces(t *testing.T) {
	r := newRig(t)
	spaced := filepath.Join(r.root, "with space", `and "quotes"`)
	r.env = append(r.env, "XDG_DATA_HOME="+spaced)
	r.setup()
	r.newSession("first")
	r.startDaemon()
	r.mustRexd("install", r.plugin(probeID, "e2e_probe", "1.0"))
	if out, err := r.runAction("e2e_probe", "first"); err != nil || !strings.Contains(out, "1.0") {
		t.Fatalf("action: %v\n%s", err, out)
	}
	r.waitRunning(probeID, 0)
	r.waitMarker("started " + probeID + " 1.0")
}

// The whole path through the command line with a real LaunchAgent: setup installs
// it, launchd starts the daemon, teardown removes it. It runs the command line
// in-process, to give it a label of its own, so a real installation stays untouched.
func TestSetupAndTeardownWithALaunchAgent(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("LaunchAgents only exist on macOS")
	}
	r := newRig(t)
	r.newSession("first")
	label := fmt.Sprintf("%s.e2e-cli.%d", service.Label, os.Getpid())
	run := func(args ...string) string {
		t.Helper()
		return r.cliInProcess(label, args...)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := service.Uninstall(ctx, label); err != nil {
			t.Errorf("cleaning up the LaunchAgent: %v", err)
		}
	})

	out := run("setup", "--server", r.server, "--rex-config", r.init)
	if !strings.Contains(out, "The daemon is running and starts at login") {
		t.Fatalf("setup output:\n%s", out)
	}
	path, err := service.PlistPath(label)
	if err != nil {
		t.Fatal(err)
	}
	plist, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plist), rexdBinary) {
		t.Errorf("the plist does not run rexd:\n%s", plist)
	}

	run("install", r.plugin(probeID, "e2e_probe", "1.0"))
	script := r.waitRunning(probeID, 0)
	r.waitMarker("started " + probeID + " 1.0")

	// Setup again must leave one daemon and one copy of each script.
	run("setup", "--server", r.server, "--rex-config", r.init)
	r.waitRunning(probeID, script.PID)
	r.newSession("once")
	r.waitMarker("session once")
	time.Sleep(200 * time.Millisecond)
	if got := r.markerCount("session once"); got != 1 {
		t.Errorf("the event was handled %d times", got)
	}

	running := r.waitRunning(probeID, 0)
	run("teardown")
	if r.status().Daemon.Running {
		t.Error("the daemon is still running")
	}
	// launchd must not bring it back. KeepAlive restarts within about 10 seconds.
	time.Sleep(11 * time.Second)
	if r.status().Daemon.Running {
		t.Error("launchd started the daemon again after teardown")
	}
	if alive(running.PID) {
		t.Error("the script outlived the teardown")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the plist is still there: %v", err)
	}
	if got, _ := os.ReadFile(r.init); strings.Contains(string(got), "rexd") {
		t.Errorf("init.lua still loads rexd: %q", got)
	}
}

// fakeOsascript puts a program named osascript first on the PATH. It writes its
// arguments to the marker file, so a test sees the notification without one
// appearing on the screen.
func (r *rig) fakeOsascript() {
	r.t.Helper()
	bin := filepath.Join(r.root, "fakebin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		r.t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"osascript $*\" >> \"$XDG_STATE_HOME/e2e-marker.txt\"\n"
	if err := os.WriteFile(filepath.Join(bin, "osascript"), []byte(script), 0o755); err != nil {
		r.t.Fatal(err)
	}
	r.env = append(r.env, "PATH="+bin+":"+os.Getenv("PATH"))
}

// The example plugin, with a real bell from a real terminal in Rex.
func TestExamplePlugin(t *testing.T) {
	r := newRig(t)
	r.fakeOsascript()
	// Config actions run inside the server, so it needs the fake on its PATH too.
	r.stopServer()
	r.startServer()
	r.setup()
	r.newSession("first")
	r.startDaemon()
	example, err := filepath.Abs("../examples/bell-notify")
	if err != nil {
		t.Fatal(err)
	}
	if out := r.mustRexd("install", example); !strings.Contains(out, "Installed rexd.bell-notify") {
		t.Fatalf("install output:\n%s", out)
	}
	r.waitRunning("rexd.bell-notify", 0)
	r.eventually("the script to listen", func() bool {
		return strings.Contains(r.mustRexd("logs", "rexd.bell-notify"), "listening for terminal bells")
	})

	// A new session whose program rings the terminal bell.
	r.rex("new", "ringing", "-c", r.root, "--json", "--", "/bin/sh", "-c", `sleep 1; printf '\a'; sleep 30`)
	r.eventually("the notification", func() bool {
		return strings.Contains(r.markerText(), "Bell in ringing")
	})

	// The action defined in its config code works too.
	if out, err := r.runAction("bell_notify_test", "first"); err != nil {
		t.Fatalf("action: %v\n%s", err, out)
	}
	r.eventually("the test notification", func() bool {
		return strings.Contains(r.markerText(), "Version 0.1.0 is working")
	})
}

// This test uses launchd for real, with a label of its own. It never touches the
// LaunchAgent of a real installation.
func TestLaunchAgent(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("LaunchAgents only exist on macOS")
	}
	r := newRig(t)
	r.setup()
	r.newSession("first")

	label := fmt.Sprintf("%s.e2e.%d", service.Label, os.Getpid())
	env := map[string]string{"PATH": os.Getenv("PATH")}
	for _, kv := range r.env {
		name, value, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "XDG_") || strings.HasPrefix(name, "REXD_") {
			env[name] = value
		}
	}
	spec := service.Spec{
		Label:   label,
		Program: []string{rexdBinary, "daemon"},
		Env:     env,
		LogPath: filepath.Join(r.root, "launchd.log"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), time.Minute)
		defer done()
		if err := service.Uninstall(cleanup, label); err != nil {
			t.Errorf("cleaning up the LaunchAgent: %v", err)
		}
	})
	if err := service.Install(ctx, spec); err != nil {
		t.Fatal(err)
	}

	// launchd starts the daemon, and the daemon runs the plugin's script.
	r.eventually("launchd to start the daemon", func() bool { return r.status().Daemon.Running })
	r.mustRexd("install", r.plugin(probeID, "e2e_probe", "1.0"))
	r.waitRunning(probeID, 0)
	r.waitMarker("started " + probeID + " 1.0")
	first := r.status().Daemon.PID

	// KeepAlive brings a killed daemon back.
	if err := syscall.Kill(first, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	r.eventually("launchd to restart the daemon", func() bool {
		got := r.status()
		return got.Daemon.Running && got.Daemon.PID != first
	})
	r.eventually("the script to run under the new daemon", func() bool {
		return r.markerCount("started "+probeID+" 1.0") == 2 && r.script(probeID).State == state.ScriptRunning
	})
	r.newSession("once")
	r.waitMarker("session once")
	time.Sleep(200 * time.Millisecond)
	if got := r.markerCount("session once"); got != 1 {
		t.Errorf("the event was handled %d times", got)
	}

	// Uninstalling stops the daemon and removes the file.
	script := r.script(probeID)
	if err := service.Uninstall(ctx, label); err != nil {
		t.Fatal(err)
	}
	r.eventually("the daemon to stop", func() bool { return !r.status().Daemon.Running })
	if alive(script.PID) {
		t.Error("the script outlived the daemon")
	}
	path, err := service.PlistPath(label)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the plist is still there: %v", err)
	}
}
