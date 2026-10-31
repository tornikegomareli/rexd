package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tornikegomareli/rexd/internal/lockfile"
	"github.com/tornikegomareli/rexd/internal/manifest"
	"github.com/tornikegomareli/rexd/internal/paths"
	"github.com/tornikegomareli/rexd/internal/state"
	"github.com/tornikegomareli/rexd/internal/supervisor"
	"github.com/tornikegomareli/rexd/internal/testutil/fakerex"
)

func TestMain(m *testing.M) {
	os.Exit(fakerex.RunTests(m))
}

const waitFor = 30 * time.Second

func testDirs(t *testing.T) paths.Dirs {
	t.Helper()
	root := t.TempDir()
	return paths.Dirs{Data: filepath.Join(root, "data"), State: filepath.Join(root, "state")}
}

// addPlugin writes a plugin with one script and records it in the state.
func addPlugin(t *testing.T, dirs paths.Dirs, id, script string, generation int) {
	t.Helper()
	dir := dirs.Plugin(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := `id = "` + id + `"
name = "Test"
version = "1.0"

[[scripts]]
name = "watch"
path = "watch.lua"
`
	if err := os.WriteFile(filepath.Join(dir, manifest.FileName), []byte(m), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "watch.lua"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	current, err := state.Load(dirs.StateFile())
	if err != nil {
		t.Fatal(err)
	}
	current.Plugins[id] = &state.Plugin{ID: id, Version: "1.0", Kind: state.KindCopy, Source: "/src", Generation: generation}
	if err := current.Save(dirs.StateFile()); err != nil {
		t.Fatal(err)
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

type runningDaemon struct {
	cancel context.CancelFunc
	done   chan error
}

func startDaemon(t *testing.T, dirs paths.Dirs, fake *fakerex.Fake) *runningDaemon {
	t.Helper()
	timing := supervisor.DefaultTiming()
	timing.ProbeInterval = 20 * time.Millisecond
	timing.ProbeMax = 20 * time.Millisecond
	timing.BackoffMin = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	d := &runningDaemon{cancel: cancel, done: make(chan error, 1)}
	go func() {
		d.done <- Run(ctx, Options{
			Dirs:    dirs,
			FindRex: func() (string, error) { return fake.Path, nil },
			RexEnv:  fake.Env,
			Timing:  &timing,
		})
	}()
	t.Cleanup(func() { d.stop(t) })
	eventually(t, "the daemon to start", func() bool {
		_, running, _ := Running(dirs)
		_, statErr := os.Stat(dirs.DaemonPID())
		return running && statErr == nil
	})
	return d
}

func (d *runningDaemon) stop(t *testing.T) error {
	t.Helper()
	d.cancel()
	select {
	case err := <-d.done:
		d.done <- err
		return err
	case <-time.After(waitFor):
		t.Fatal("the daemon did not stop")
		return nil
	}
}

func scriptState(dirs paths.Dirs, plugin string) state.ScriptStatus {
	status, err := state.LoadStatus(dirs.StatusFile())
	if err != nil || status == nil {
		return state.ScriptStatus{}
	}
	for _, s := range status.Scripts {
		if s.Plugin == plugin {
			return s
		}
	}
	return state.ScriptStatus{}
}

func TestRunStartsPluginScriptsWithTheirEnvironment(t *testing.T) {
	dirs := testDirs(t)
	fake := fakerex.New(t)
	addPlugin(t, dirs, "acme.one", "print ready\n", 1)
	startDaemon(t, dirs, fake)

	eventually(t, "the script to run", func() bool {
		return scriptState(dirs, "acme.one").State == state.ScriptRunning && len(fake.Starts()) == 1
	})
	start := fake.Starts()[0]
	if start.Plugin != "acme.one" {
		t.Errorf("REXD_PLUGIN_ID = %q", start.Plugin)
	}
	wantDir, _ := filepath.EvalSymlinks(dirs.Plugin("acme.one"))
	if gotDir, _ := filepath.EvalSymlinks(start.Dir); gotDir != wantDir {
		t.Errorf("dir = %q, want %q", gotDir, wantDir)
	}
	if start.Script != filepath.Join(dirs.Plugin("acme.one"), "watch.lua") {
		t.Errorf("script = %q", start.Script)
	}
	eventually(t, "the script's log", func() bool {
		data, _ := os.ReadFile(dirs.ScriptLog("acme.one", "watch"))
		return strings.Contains(string(data), "ready")
	})
}

func TestSecondDaemonIsRefused(t *testing.T) {
	dirs := testDirs(t)
	startDaemon(t, dirs, fakerex.New(t))
	err := Run(context.Background(), Options{Dirs: dirs})
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("err = %v", err)
	}
}

func TestStartIsNotConfusedByARunningCheck(t *testing.T) {
	dirs := testDirs(t)
	// A check holds the lock for an instant. Here it holds it longer, to be sure.
	check, err := lockfile.TryAcquire(dirs.DaemonLock())
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(150*time.Millisecond, func() { check.Release() })

	lock, err := acquireDaemonLock(dirs)
	if err != nil {
		t.Fatalf("the daemon lost to a check: %v", err)
	}
	lock.Release()
}

func TestRunning(t *testing.T) {
	dirs := testDirs(t)
	if _, running, err := Running(dirs); running || err != nil {
		t.Fatalf("before start: running %v, err %v", running, err)
	}
	d := startDaemon(t, dirs, fakerex.New(t))
	pid, running, err := Running(dirs)
	if err != nil || !running || pid != os.Getpid() {
		t.Fatalf("while running: pid %d, running %v, err %v", pid, running, err)
	}
	if err := d.stop(t); err != nil {
		t.Fatal(err)
	}
	if _, running, _ := Running(dirs); running {
		t.Fatal("still running after stop")
	}
	if _, err := os.Stat(dirs.DaemonPID()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pid file left behind: %v", err)
	}
}

func TestNotifyWithoutADaemon(t *testing.T) {
	notified, err := Notify(testDirs(t))
	if notified || err != nil {
		t.Fatalf("notified %v, err %v", notified, err)
	}
}

func TestNotifyMakesTheDaemonReload(t *testing.T) {
	dirs := testDirs(t)
	fake := fakerex.New(t)
	startDaemon(t, dirs, fake)

	addPlugin(t, dirs, "acme.new", "", 1)
	notified, err := Notify(dirs)
	if err != nil || !notified {
		t.Fatalf("notified %v, err %v", notified, err)
	}
	eventually(t, "the new script to run", func() bool {
		return scriptState(dirs, "acme.new").State == state.ScriptRunning
	})

	current, _ := state.Load(dirs.StateFile())
	delete(current.Plugins, "acme.new")
	if err := current.Save(dirs.StateFile()); err != nil {
		t.Fatal(err)
	}
	if _, err := Notify(dirs); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the script to stop", func() bool {
		return scriptState(dirs, "acme.new").State == ""
	})
}

func TestNewGenerationRestartsTheScript(t *testing.T) {
	dirs := testDirs(t)
	fake := fakerex.New(t)
	addPlugin(t, dirs, "acme.one", "", 1)
	startDaemon(t, dirs, fake)
	var pid int
	eventually(t, "the script to run", func() bool {
		s := scriptState(dirs, "acme.one")
		pid = s.PID
		return s.State == state.ScriptRunning
	})

	addPlugin(t, dirs, "acme.one", "", 2)
	if _, err := Notify(dirs); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a new process", func() bool {
		s := scriptState(dirs, "acme.one")
		return s.State == state.ScriptRunning && s.PID != pid
	})
}

func TestSIGTERMStopsTheDaemonAndItsScripts(t *testing.T) {
	dirs := testDirs(t)
	fake := fakerex.New(t)
	addPlugin(t, dirs, "acme.one", "", 1)
	d := startDaemon(t, dirs, fake)
	var pid int
	eventually(t, "the script to run", func() bool {
		s := scriptState(dirs, "acme.one")
		pid = s.PID
		return s.State == state.ScriptRunning
	})

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-d.done:
		d.done <- err
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(waitFor):
		t.Fatal("the daemon did not stop on SIGTERM")
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatal("the script outlived the daemon")
	}
}

func TestStop(t *testing.T) {
	dirs := testDirs(t)
	if stopped, err := Stop(context.Background(), dirs, time.Second); stopped || err != nil {
		t.Fatalf("without a daemon: stopped %v, err %v", stopped, err)
	}
	d := startDaemon(t, dirs, fakerex.New(t))
	stopped, err := Stop(context.Background(), dirs, waitFor)
	if err != nil || !stopped {
		t.Fatalf("stopped %v, err %v", stopped, err)
	}
	if _, running, _ := Running(dirs); running {
		t.Fatal("still running")
	}
	if err := d.stop(t); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDesired(t *testing.T) {
	dirs := testDirs(t)
	addPlugin(t, dirs, "acme.good", "", 3)
	current, _ := state.Load(dirs.StateFile())
	current.Settings.Server = "unix:///s.sock"
	current.Plugins["acme.gone"] = &state.Plugin{ID: "acme.gone", Version: "1", Generation: 1}
	if err := current.Save(dirs.StateFile()); err != nil {
		t.Fatal(err)
	}

	got, err := LoadDesired(dirs, func() (string, error) { return "/bin/rex", nil }, []string{"X=1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Client.Path != "/bin/rex" || got.Client.Server != "unix:///s.sock" || !slices.Equal(got.Client.Env, []string{"X=1"}) {
		t.Errorf("client = %+v", got.Client)
	}
	if len(got.Specs) != 1 {
		t.Fatalf("specs = %+v", got.Specs)
	}
	spec := got.Specs[0]
	if spec.Plugin != "acme.good" || spec.Script != "watch" {
		t.Errorf("spec = %+v", spec)
	}
	for _, want := range []string{
		"REXD_PLUGIN_ID=acme.good",
		"REXD_PLUGIN_VERSION=1.0",
		"REXD_PLUGIN_DIR=" + dirs.Plugin("acme.good"),
		"REXD_SCRIPT=watch",
	} {
		if !slices.Contains(spec.Env, want) {
			t.Errorf("env %v has no %q", spec.Env, want)
		}
	}
	if !strings.Contains(got.PluginErrors["acme.gone"], "not a rexd plugin") {
		t.Errorf("plugin errors = %v", got.PluginErrors)
	}
}

func TestLoadDesiredReportsAMissingRex(t *testing.T) {
	_, err := LoadDesired(testDirs(t), func() (string, error) { return "", errors.New("no rex") }, nil)
	if err == nil || err.Error() != "no rex" {
		t.Fatalf("err = %v", err)
	}
}

func TestFingerprintChangesWithGenerationAndServer(t *testing.T) {
	script := manifest.Script{Name: "watch", Path: "watch.lua"}
	base := fingerprint("", &state.Plugin{Version: "1", Generation: 1}, script)
	if base != fingerprint("", &state.Plugin{Version: "1", Generation: 1}, script) {
		t.Fatal("fingerprint is not stable")
	}
	for name, other := range map[string]string{
		"generation": fingerprint("", &state.Plugin{Version: "1", Generation: 2}, script),
		"version":    fingerprint("", &state.Plugin{Version: "2", Generation: 1}, script),
		"server":     fingerprint("unix:///x", &state.Plugin{Version: "1", Generation: 1}, script),
		"path":       fingerprint("", &state.Plugin{Version: "1", Generation: 1}, manifest.Script{Name: "watch", Path: "new.lua"}),
	} {
		if other == base {
			t.Errorf("%s change did not change the fingerprint", name)
		}
	}
}
