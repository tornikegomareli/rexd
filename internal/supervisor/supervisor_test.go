package supervisor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/tornikegomareli/rexd/internal/rex"
	"github.com/tornikegomareli/rexd/internal/state"
	"github.com/tornikegomareli/rexd/internal/testutil/fakerex"
)

func TestMain(m *testing.M) {
	os.Exit(fakerex.RunTests(m))
}

// waitFor is generous because each fake rex is a new process of a race-enabled binary.
const waitFor = 30 * time.Second

func fastTiming() Timing {
	return Timing{
		BackoffMin:    10 * time.Millisecond,
		BackoffMax:    50 * time.Millisecond,
		ResetAfter:    time.Hour,
		CrashLimit:    3,
		CrashWindow:   time.Hour,
		ProbeInterval: 20 * time.Millisecond,
		ProbeTimeout:  waitFor,
		StopTimeout:   2 * time.Second,
	}
}

type harness struct {
	t      *testing.T
	fake   *fakerex.Fake
	dir    string
	status string
	reload chan struct{}
	cancel context.CancelFunc
	done   chan error

	mu      sync.Mutex
	specs   []Spec
	loadErr error
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	return &harness{
		t:      t,
		fake:   fakerex.New(t),
		dir:    dir,
		status: filepath.Join(dir, "status.json"),
		reload: make(chan struct{}),
		done:   make(chan error, 1),
	}
}

// script writes a script of fake rex directives and returns its spec.
func (h *harness) script(name, body string) Spec {
	h.t.Helper()
	path := filepath.Join(h.dir, name+".lua")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		h.t.Fatal(err)
	}
	return Spec{
		Plugin:      "acme.test",
		Script:      name,
		Path:        path,
		Dir:         h.dir,
		Env:         []string{"REXD_PLUGIN_ID=acme.test"},
		LogPath:     filepath.Join(h.dir, "logs", name+".log"),
		Fingerprint: "1",
	}
}

func (h *harness) setSpecs(specs ...Spec) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.specs = specs
}

func (h *harness) run(timing Timing) {
	h.t.Helper()
	sup := New(Options{
		Load: func() (Desired, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return Desired{
				Client:       rex.Client{Path: h.fake.Path, Env: h.fake.Env},
				Specs:        slices.Clone(h.specs),
				PluginErrors: map[string]string{},
			}, h.loadErr
		},
		StatusPath: h.status,
		Timing:     timing,
	})
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.done <- sup.Run(ctx, h.reload) }()
	h.t.Cleanup(h.shutdown)
}

// shutdown stops the supervisor. It is safe to call more than once.
func (h *harness) shutdown() {
	h.t.Helper()
	h.cancel()
	select {
	case err := <-h.done:
		if err != nil {
			h.t.Errorf("Run: %v", err)
		}
		h.done <- nil
	case <-time.After(waitFor):
		h.t.Fatal("Run did not return")
	}
}

func (h *harness) kick() {
	h.t.Helper()
	select {
	case h.reload <- struct{}{}:
	case <-time.After(waitFor):
		h.t.Fatal("the supervisor did not take the reload")
	}
}

func (h *harness) eventually(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	status, _ := os.ReadFile(h.status)
	h.t.Fatalf("timed out waiting for: %s\nstatus: %s\nstarts: %+v", what, status, h.fake.Starts())
}

func (h *harness) scriptStatus(name string) (state.ScriptStatus, bool) {
	status, err := state.LoadStatus(h.status)
	if err != nil || status == nil {
		return state.ScriptStatus{}, false
	}
	for _, s := range status.Scripts {
		if s.Script == name {
			return s, true
		}
	}
	return state.ScriptStatus{}, false
}

func (h *harness) waitState(name string, want ...state.ScriptState) state.ScriptStatus {
	h.t.Helper()
	var got state.ScriptStatus
	h.eventually(name+" to be "+string(want[0]), func() bool {
		s, ok := h.scriptStatus(name)
		got = s
		return ok && slices.Contains(want, s.State)
	})
	return got
}

// waitStart waits for the fake to record its first script start. The status says
// running as soon as the process exists, which is a moment before that.
func (h *harness) waitStart() fakerex.Start {
	h.t.Helper()
	h.eventually("the script to start", func() bool { return len(h.fake.Starts()) > 0 })
	return h.fake.Starts()[0]
}

func (h *harness) rexState() state.RexState {
	status, err := state.LoadStatus(h.status)
	if err != nil || status == nil {
		return state.RexUnknown
	}
	return status.Daemon.Rex
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func TestStartsAScript(t *testing.T) {
	h := newHarness(t)
	h.fake.SetSessions("session:first", "session:second")
	spec := h.script("watch", "print hello from the script\n")
	h.setSpecs(spec)
	h.run(fastTiming())

	s := h.waitState("watch", state.ScriptRunning)
	if s.PID == 0 || s.Plugin != "acme.test" || s.Restarts != 0 {
		t.Fatalf("status = %+v", s)
	}
	if h.rexState() != state.RexUp {
		t.Fatalf("rex = %q", h.rexState())
	}

	got := h.waitStart()
	if got.Session != "session:first" {
		t.Errorf("session = %q, want the first one", got.Session)
	}
	if got.Plugin != "acme.test" {
		t.Errorf("plugin variable = %q", got.Plugin)
	}
	wantDir, _ := filepath.EvalSymlinks(h.dir)
	if gotDir, _ := filepath.EvalSymlinks(got.Dir); gotDir != wantDir {
		t.Errorf("dir = %q, want %q", gotDir, wantDir)
	}

	h.eventually("the script's output in its log", func() bool {
		data, _ := os.ReadFile(spec.LogPath)
		return strings.Contains(string(data), "hello from the script")
	})
	data, _ := os.ReadFile(spec.LogPath)
	if !strings.Contains(string(data), "rexd: started") {
		t.Errorf("log = %q", data)
	}
}

func TestPassesTheServerToRex(t *testing.T) {
	h := newHarness(t)
	h.setSpecs(h.script("watch", ""))
	sup := New(Options{
		Load: func() (Desired, error) {
			return Desired{
				Client: rex.Client{Path: h.fake.Path, Env: h.fake.Env, Server: "unix:///custom.sock"},
				Specs:  h.specs,
			}, nil
		},
		StatusPath: h.status,
		Timing:     fastTiming(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.done <- sup.Run(ctx, h.reload) }()
	t.Cleanup(h.shutdown)

	h.waitState("watch", state.ScriptRunning)
	for _, call := range h.fake.Calls() {
		if !slices.Contains(call, "unix:///custom.sock") || !slices.Contains(call, "--autostart=false") {
			t.Errorf("call without -S or --autostart=false: %v", call)
		}
	}
}

func TestRemovingAScriptStopsIt(t *testing.T) {
	h := newHarness(t)
	h.setSpecs(h.script("watch", ""))
	h.run(fastTiming())
	pid := h.waitState("watch", state.ScriptRunning).PID

	h.setSpecs()
	h.kick()
	h.eventually("the script to be gone", func() bool {
		_, present := h.scriptStatus("watch")
		return !present && !processAlive(pid)
	})
}

func TestAddingAScriptLeavesOthersAlone(t *testing.T) {
	h := newHarness(t)
	one := h.script("one", "")
	h.setSpecs(one)
	h.run(fastTiming())
	pid := h.waitState("one", state.ScriptRunning).PID

	h.setSpecs(one, h.script("two", ""))
	h.kick()
	h.waitState("two", state.ScriptRunning)
	if s, _ := h.scriptStatus("one"); s.PID != pid {
		t.Fatalf("script one restarted: pid %d, was %d", s.PID, pid)
	}
}

func TestChangedFingerprintRestartsTheScript(t *testing.T) {
	h := newHarness(t)
	spec := h.script("watch", "")
	h.setSpecs(spec)
	h.run(fastTiming())
	pid := h.waitState("watch", state.ScriptRunning).PID

	spec.Fingerprint = "2"
	h.setSpecs(spec)
	h.kick()
	h.eventually("a new process", func() bool {
		s, ok := h.scriptStatus("watch")
		return ok && s.State == state.ScriptRunning && s.PID != pid
	})
	if s, _ := h.scriptStatus("watch"); s.Restarts != 0 {
		t.Errorf("an update is not a crash: restarts = %d", s.Restarts)
	}
	if processAlive(pid) {
		t.Error("the old process is still running")
	}
}

func TestUnchangedFingerprintKeepsTheScript(t *testing.T) {
	h := newHarness(t)
	spec := h.script("watch", "")
	h.setSpecs(spec)
	h.run(fastTiming())
	pid := h.waitState("watch", state.ScriptRunning).PID

	h.kick()
	h.kick() // The second send proves the first reload has finished.
	if s, _ := h.scriptStatus("watch"); s.PID != pid {
		t.Fatalf("restarted without a reason: pid %d, was %d", s.PID, pid)
	}
}

func TestRestartsACrashedScript(t *testing.T) {
	h := newHarness(t)
	h.setSpecs(h.script("crash", "exit 1\n"))
	h.run(fastTiming())

	h.eventually("at least one restart", func() bool {
		s, ok := h.scriptStatus("crash")
		return ok && s.Restarts >= 1
	})
	s, _ := h.scriptStatus("crash")
	if s.LastExit == nil || s.LastExit.Code != 1 {
		t.Fatalf("last exit = %+v", s.LastExit)
	}
}

func TestRepeatedExitsBecomeCrashing(t *testing.T) {
	h := newHarness(t)
	timing := fastTiming()
	timing.BackoffMax = 200 * time.Millisecond
	h.setSpecs(h.script("crash", "exit 1\n"))
	h.run(timing)

	s := h.waitState("crash", state.ScriptCrashing)
	if s.NextStart.IsZero() {
		t.Errorf("a crashing script still has a next start: %+v", s)
	}
	// It keeps trying.
	seen := len(h.fake.Starts())
	h.eventually("another attempt", func() bool { return len(h.fake.Starts()) > seen })
}

func TestScriptExitingCleanlyIsRestartedToo(t *testing.T) {
	h := newHarness(t)
	h.setSpecs(h.script("done", "exit 0\n"))
	h.run(fastTiming())
	h.eventually("a restart", func() bool {
		s, ok := h.scriptStatus("done")
		return ok && s.Restarts >= 1 && s.LastExit != nil && s.LastExit.Code == 0
	})
}

func TestWaitsWhileRexIsDown(t *testing.T) {
	h := newHarness(t)
	h.fake.SetDown(true)
	h.setSpecs(h.script("watch", ""))
	h.run(fastTiming())

	h.waitState("watch", state.ScriptWaiting)
	h.eventually("rex to be down", func() bool { return h.rexState() == state.RexDown })
	if n := len(h.fake.Starts()); n != 0 {
		t.Fatalf("started %d times with Rex down", n)
	}

	h.fake.SetDown(false)
	s := h.waitState("watch", state.ScriptRunning)
	if s.Restarts != 0 || s.LastExit != nil {
		t.Fatalf("waiting is not failing: %+v", s)
	}
}

func TestWaitsForAFirstSession(t *testing.T) {
	h := newHarness(t)
	h.fake.SetSessions()
	h.setSpecs(h.script("watch", ""))
	h.run(fastTiming())

	h.eventually("no sessions to be reported", func() bool { return h.rexState() == state.RexNoSessions })
	h.waitState("watch", state.ScriptWaiting)

	h.fake.SetSessions("session:new")
	h.waitState("watch", state.ScriptRunning)
	if got := h.waitStart().Session; got != "session:new" {
		t.Fatalf("session = %q", got)
	}
}

func TestRexGoingAwayIsNotAFailure(t *testing.T) {
	h := newHarness(t)
	h.setSpecs(h.script("watch", ""))
	h.run(fastTiming())
	pid := h.waitState("watch", state.ScriptRunning).PID

	h.fake.SetDown(true)
	h.eventually("the script to wait for Rex", func() bool {
		s, ok := h.scriptStatus("watch")
		return ok && s.State == state.ScriptWaiting && h.rexState() == state.RexDown
	})
	if processAlive(pid) {
		t.Fatal("the process should have exited")
	}

	h.fake.SetDown(false)
	s := h.waitState("watch", state.ScriptRunning)
	if s.PID == pid {
		t.Fatal("expected a new process")
	}
	if s.Restarts != 0 || s.LastExit != nil {
		t.Fatalf("Rex restarting is not the script's fault: %+v", s)
	}
}

func TestStopEscalatesToSIGKILL(t *testing.T) {
	h := newHarness(t)
	timing := fastTiming()
	timing.StopTimeout = 100 * time.Millisecond
	spec := h.script("stubborn", "ignore-int\nprint ready\n")
	h.setSpecs(spec)
	h.run(timing)
	pid := h.waitState("stubborn", state.ScriptRunning).PID
	h.eventually("the script to ignore SIGINT", func() bool {
		data, _ := os.ReadFile(spec.LogPath)
		return strings.Contains(string(data), "ready")
	})

	h.setSpecs()
	h.kick()
	h.eventually("the process to be killed", func() bool {
		_, present := h.scriptStatus("stubborn")
		return !present && !processAlive(pid)
	})
}

func TestShutdownStopsEverything(t *testing.T) {
	h := newHarness(t)
	h.setSpecs(h.script("one", ""), h.script("two", ""))
	h.run(fastTiming())
	one := h.waitState("one", state.ScriptRunning).PID
	two := h.waitState("two", state.ScriptRunning).PID

	h.shutdown()

	if processAlive(one) || processAlive(two) {
		t.Fatal("processes outlived the supervisor")
	}
	if _, err := os.Stat(h.status); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status file should be gone: %v", err)
	}
}

func TestStopsScriptsLeftByADeadDaemon(t *testing.T) {
	h := newHarness(t)
	spec := h.script("watch", "")

	orphan := exec.Command(h.fake.Path, "do", spec.Path, "--all")
	orphan.Env = append(os.Environ(), append(h.fake.Env, "REX_SESSION=session:one")...)
	orphan.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := orphan.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { orphan.Wait(); close(exited) }()
	h.eventually("the orphan to run", func() bool { return len(h.fake.Starts()) == 1 })

	previous := &state.Status{Scripts: []state.ScriptStatus{{
		Plugin: spec.Plugin, Script: spec.Script, Path: spec.Path, State: state.ScriptRunning, PID: orphan.Process.Pid,
	}}}
	if err := previous.Save(h.status); err != nil {
		t.Fatal(err)
	}

	h.setSpecs(spec)
	h.run(fastTiming())

	select {
	case <-exited:
	case <-time.After(waitFor):
		orphan.Process.Kill()
		t.Fatal("the orphan was not stopped")
	}
	h.eventually("a new process to replace the orphan", func() bool {
		s, ok := h.scriptStatus("watch")
		return ok && s.State == state.ScriptRunning && s.PID != orphan.Process.Pid
	})
}

func TestLeavesAnUnrelatedProcessAlone(t *testing.T) {
	h := newHarness(t)
	spec := h.script("watch", "")

	other := exec.Command("sleep", "60")
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Process.Kill(); other.Wait() })

	// The process ID in the old status file now belongs to something else.
	previous := &state.Status{Scripts: []state.ScriptStatus{{
		Plugin: spec.Plugin, Script: spec.Script, Path: spec.Path, State: state.ScriptRunning, PID: other.Process.Pid,
	}}}
	if err := previous.Save(h.status); err != nil {
		t.Fatal(err)
	}

	h.setSpecs(spec)
	h.run(fastTiming())
	h.eventually("the script to start", func() bool {
		s, ok := h.scriptStatus("watch")
		return ok && s.State == state.ScriptRunning && s.PID != other.Process.Pid
	})
	if !processAlive(other.Process.Pid) {
		t.Fatal("killed a process that was not a leftover script")
	}
}

func TestLoadErrorKeepsWhatIsRunning(t *testing.T) {
	h := newHarness(t)
	h.setSpecs(h.script("watch", ""))
	h.run(fastTiming())
	pid := h.waitState("watch", state.ScriptRunning).PID

	h.mu.Lock()
	h.loadErr = errors.New("state file is broken")
	h.mu.Unlock()
	h.kick()
	h.kick()
	if s, ok := h.scriptStatus("watch"); !ok || s.PID != pid {
		t.Fatalf("status = %+v", s)
	}
	if !processAlive(pid) {
		t.Fatal("the script was stopped")
	}
}

func TestAScriptThatCannotStartIsRetried(t *testing.T) {
	h := newHarness(t)
	spec := h.script("gone", "")
	spec.Dir = filepath.Join(h.dir, "no-such-folder")
	h.setSpecs(spec)
	h.run(fastTiming())

	h.eventually("a recorded failure", func() bool {
		s, ok := h.scriptStatus("gone")
		return ok && s.LastExit != nil && s.LastExit.Code == -1 && s.LastExit.Error != ""
	})
}

func TestReportsPluginErrors(t *testing.T) {
	h := newHarness(t)
	sup := New(Options{
		Load: func() (Desired, error) {
			return Desired{
				Client:       rex.Client{Path: h.fake.Path, Env: h.fake.Env},
				PluginErrors: map[string]string{"acme.broken": "no manifest"},
			}, nil
		},
		StatusPath: h.status,
		Timing:     fastTiming(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.done <- sup.Run(ctx, h.reload) }()
	t.Cleanup(h.shutdown)

	h.eventually("the plugin error in the status", func() bool {
		status, err := state.LoadStatus(h.status)
		return err == nil && status != nil && status.PluginErrors["acme.broken"] == "no manifest"
	})
}

func TestNextDelay(t *testing.T) {
	low, high := time.Second, time.Minute
	got := time.Duration(0)
	var seen []time.Duration
	for range 8 {
		got = nextDelay(got, low, high)
		seen = append(seen, got)
	}
	want := []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 32 * time.Second, time.Minute, time.Minute,
	}
	if !slices.Equal(seen, want) {
		t.Fatalf("got %v", seen)
	}
}

func TestDefaultTimingMatchesTheDocs(t *testing.T) {
	timing := DefaultTiming()
	if timing.BackoffMin != time.Second || timing.BackoffMax != time.Minute || timing.ResetAfter != time.Minute ||
		timing.CrashLimit != 5 || timing.CrashWindow != 2*time.Minute || timing.StopTimeout != 5*time.Second {
		t.Fatalf("timing = %+v", timing)
	}
}
