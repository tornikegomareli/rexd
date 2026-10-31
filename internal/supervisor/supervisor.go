// Package supervisor keeps event scripts running.
//
// A script needs a Rex server with at least one session. While that is missing,
// scripts wait, and waiting does not count as failing. The supervisor learns
// about Rex by asking it with rex ls. Once Rex is there, a script that exits is
// a failed script: it restarts after a delay that doubles up to a limit.
//
// One goroutine, the one in Run, owns all state. Everything else posts functions
// for it to run, so nothing needs a lock.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tornikegomareli/rexd/internal/logfile"
	"github.com/tornikegomareli/rexd/internal/rex"
	"github.com/tornikegomareli/rexd/internal/state"
)

// waitDelay limits how long Wait keeps reading output after a script has exited.
// Without it, a process the script left running could hold the output open forever.
const waitDelay = 2 * time.Second

// Spec says what to run for one event script.
type Spec struct {
	Plugin string
	Script string
	// Path is the script's absolute path.
	Path string
	// Dir is the working directory.
	Dir string
	// Env holds variables added to the script's environment.
	Env []string
	// LogPath receives the script's output.
	LogPath string
	// Fingerprint changes when the script must restart, such as after an update.
	Fingerprint string
}

func (s Spec) key() string { return s.Plugin + "/" + s.Script }

// Desired is what should be running.
type Desired struct {
	// Client runs rex commands.
	Client rex.Client
	Specs  []Spec
	// PluginErrors maps a plugin id to why its scripts could not be read.
	PluginErrors map[string]string
}

// Timing sets the supervisor's delays and limits.
type Timing struct {
	// BackoffMin is the delay before the first restart.
	BackoffMin time.Duration
	// BackoffMax is the longest delay between restarts.
	BackoffMax time.Duration
	// ResetAfter is how long a script must run for its delay to start over.
	ResetAfter time.Duration
	// CrashLimit is how many failures within CrashWindow make a script crashing.
	CrashLimit  int
	CrashWindow time.Duration
	// ProbeInterval is the first delay before asking Rex again while scripts wait
	// for it. Rex can stay away for hours, such as overnight, and each question
	// starts a process, so the delay doubles up to ProbeMax.
	ProbeInterval time.Duration
	// ProbeMax is the longest delay between questions. It is also the longest a
	// script waits to start after Rex comes back.
	ProbeMax time.Duration
	// ProbeTimeout limits one question to Rex.
	ProbeTimeout time.Duration
	// StopTimeout is how long a script gets to exit after SIGINT, before SIGKILL.
	StopTimeout time.Duration
}

// DefaultTiming returns the delays and limits for normal use.
func DefaultTiming() Timing {
	return Timing{
		BackoffMin:    time.Second,
		BackoffMax:    time.Minute,
		ResetAfter:    time.Minute,
		CrashLimit:    5,
		CrashWindow:   2 * time.Minute,
		ProbeInterval: 2 * time.Second,
		ProbeMax:      15 * time.Second,
		ProbeTimeout:  10 * time.Second,
		StopTimeout:   5 * time.Second,
	}
}

// Options configures a Supervisor.
type Options struct {
	// Load returns what should be running. It is called at start and on every reload.
	Load func() (Desired, error)
	// StatusPath is where the supervisor reports what is running.
	StatusPath string
	Logger     *slog.Logger
	Timing     Timing
}

// Supervisor keeps scripts running. Use New, then Run.
type Supervisor struct {
	opts Options
	log  *slog.Logger

	events chan func()
	done   chan struct{}

	client       rex.Client
	units        map[string]*unit
	pluginErrors map[string]string

	rexState     state.RexState
	probing      bool
	probeAgain   bool
	probeTimer   *time.Timer
	probeDelay   time.Duration
	lastProbeErr string

	// problem says why Load failed. reloadTimer tries again.
	problem     string
	reloadTimer *time.Timer
	stopping    bool

	startedAt time.Time
	dirty     bool
}

// unit is one script and everything known about it.
type unit struct {
	spec  Spec
	state state.ScriptState
	since time.Time

	// run counts starts. Timers and exit reports carry the run they belong to,
	// so a late one for an earlier run is ignored.
	run       int
	pid       int
	startedAt time.Time
	lastRan   time.Duration
	log       *logfile.Writer

	delay      time.Duration
	failures   []time.Time
	restarts   int
	restartDue bool
	nextStart  time.Time
	timer      *time.Timer
	killTimer  *time.Timer
	lastExit   *state.Exit
	// exitToJudge means the script exited and the next answer from Rex decides
	// whether that was a failure or Rex going away.
	exitToJudge bool

	// removing means the unit is deleted once its process has exited.
	removing bool
	// pending replaces the spec once the process has exited.
	pending *Spec
}

// New returns a Supervisor.
func New(opts Options) *Supervisor {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	return &Supervisor{
		opts:   opts,
		log:    opts.Logger,
		events: make(chan func(), 64),
		done:   make(chan struct{}),
		units:  map[string]*unit{},
	}
}

// Run supervises scripts until ctx ends, then stops them and returns. A value on
// reload makes it call Load again and start or stop scripts to match.
func (s *Supervisor) Run(ctx context.Context, reload <-chan struct{}) error {
	defer close(s.done)
	s.startedAt = time.Now()
	s.dirty = true
	s.stopOrphans()
	s.reconcile()
	s.flushStatus()
	for {
		select {
		case <-ctx.Done():
			s.shutdown()
			return nil
		case <-reload:
			s.reconcile()
		case event := <-s.events:
			event()
		}
		s.flushStatus()
	}
}

// post asks the Run goroutine to call event.
func (s *Supervisor) post(event func()) {
	select {
	case s.events <- event:
	case <-s.done:
	}
}

// stopOrphans stops scripts left behind by a daemon that died without stopping
// them. Scripts have their own process groups, so nothing else cleans them up,
// and with a second copy running every event would be handled twice. The last
// status file says which processes those were. A process only counts if its
// command line still names the script, because its ID may belong to something
// else by now.
func (s *Supervisor) stopOrphans() {
	previous, err := state.LoadStatus(s.opts.StatusPath)
	if err != nil {
		s.log.Warn("could not read the previous status file", "error", err)
		return
	}
	if previous == nil {
		return
	}
	var orphans []int
	for _, script := range previous.Scripts {
		if script.PID == 0 || script.Path == "" || !runsScript(script.PID, script.Path) {
			continue
		}
		s.log.Warn("stopping a script left by an earlier daemon", "plugin", script.Plugin, "script", script.Script, "pid", script.PID)
		signalGroup(script.PID, syscall.SIGINT)
		orphans = append(orphans, script.PID)
	}
	deadline := time.Now().Add(s.opts.Timing.StopTimeout)
	for _, pid := range orphans {
		for alive(pid) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if alive(pid) {
			signalGroup(pid, syscall.SIGKILL)
		}
	}
}

func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// runsScript reports whether the process's command line names the script.
func runsScript(pid int, path string) bool {
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && strings.Contains(string(out), path)
}

// reconcile starts and stops scripts to match what Load returns.
func (s *Supervisor) reconcile() {
	if s.stopping {
		return
	}
	stopTimer(s.reloadTimer)
	desired, err := s.opts.Load()
	if err != nil {
		if message := err.Error(); message != s.problem {
			s.log.Error("could not read what should run; keeping what is running", "error", err)
			s.problem = message
			s.dirty = true
		}
		s.scheduleReload()
		return
	}
	if s.problem != "" {
		s.log.Info("can read what should run again")
		s.problem = ""
	}
	s.client = desired.Client
	s.pluginErrors = desired.PluginErrors
	s.dirty = true

	wanted := map[string]bool{}
	for _, spec := range desired.Specs {
		wanted[spec.key()] = true
		u := s.units[spec.key()]
		switch {
		case u == nil:
			s.units[spec.key()] = &unit{spec: spec, state: state.ScriptWaiting, since: time.Now()}
		case u.pid != 0:
			if u.spec.Fingerprint != spec.Fingerprint || u.removing || u.pending != nil {
				pending := spec
				u.pending, u.removing = &pending, false
				s.stop(u)
			}
		case u.spec.Fingerprint != spec.Fingerprint:
			s.reset(u, spec)
		}
	}
	for key, u := range s.units {
		if wanted[key] {
			continue
		}
		u.removing, u.pending = true, nil
		if u.pid != 0 {
			s.stop(u)
		} else {
			s.drop(u)
		}
	}
	if s.anyWaiting() {
		s.requestProbe()
	}
}

// scheduleReload tries Load again later. No one may ever send a reload: when the
// daemon starts at login before Rex is installed, nothing else wakes it.
func (s *Supervisor) scheduleReload() {
	stopTimer(s.reloadTimer)
	s.reloadTimer = time.AfterFunc(s.opts.Timing.ProbeMax, func() { s.post(s.reconcile) })
}

// reset gives a stopped unit a new spec and a clean record.
func (s *Supervisor) reset(u *unit, spec Spec) {
	stopTimer(u.timer)
	u.run++ // Makes any report still on its way stale.
	u.spec = spec
	u.delay, u.failures, u.restarts, u.restartDue, u.lastExit, u.exitToJudge = 0, nil, 0, false, nil, false
	s.setState(u, state.ScriptWaiting)
}

// drop forgets a unit that has no process.
func (s *Supervisor) drop(u *unit) {
	stopTimer(u.timer)
	stopTimer(u.killTimer)
	if u.log != nil {
		u.log.Close()
	}
	delete(s.units, u.spec.key())
	s.dirty = true
	s.log.Info("script removed", "plugin", u.spec.Plugin, "script", u.spec.Script)
}

func (s *Supervisor) anyWaiting() bool {
	for _, u := range s.units {
		if u.state == state.ScriptWaiting {
			return true
		}
	}
	return false
}

// requestProbe asks Rex whether it is there. A request during a probe discards
// that probe's answer, because something changed after the question was asked.
func (s *Supervisor) requestProbe() {
	if s.probing {
		s.probeAgain = true
		return
	}
	stopTimer(s.probeTimer)
	s.probing = true
	client := s.client
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), s.opts.Timing.ProbeTimeout)
		defer cancel()
		sessions, err := client.Sessions(ctx)
		s.post(func() { s.handleProbe(sessions, err) })
	}()
}

func (s *Supervisor) handleProbe(sessions []string, err error) {
	s.probing = false
	if s.probeAgain {
		s.probeAgain = false
		s.requestProbe()
		return
	}

	next := state.RexUp
	switch {
	case err != nil:
		next = state.RexDown
		// Rex being closed is normal. Anything else is worth one line, not one
		// line per question.
		if message := err.Error(); !errors.Is(err, rex.ErrNotRunning) && message != s.lastProbeErr {
			s.log.Warn("could not ask Rex", "error", err)
			s.lastProbeErr = message
		}
		// The rex binary itself may have moved, and Load finds it again.
		if !errors.Is(err, rex.ErrNotRunning) {
			s.scheduleReload()
		}
	case len(sessions) == 0:
		next = state.RexNoSessions
	}
	if err == nil {
		s.lastProbeErr = ""
	}
	if next != s.rexState {
		s.log.Info("Rex changed", "from", s.rexState, "to", next)
		s.rexState = next
		s.probeDelay = 0
		s.dirty = true
	}

	for _, key := range s.sortedKeys() {
		u := s.units[key]
		if u.state != state.ScriptWaiting {
			continue
		}
		if u.exitToJudge {
			u.exitToJudge = false
			if next == state.RexUp {
				s.fail(u)
				continue
			}
			u.lastExit = nil // Rex went away, so the script did nothing wrong.
			s.dirty = true
		}
		if next == state.RexUp {
			s.start(u, sessions[0])
		}
	}

	if s.anyWaiting() {
		timing := s.opts.Timing
		s.probeDelay = nextDelay(s.probeDelay, timing.ProbeInterval, timing.ProbeMax)
		stopTimer(s.probeTimer)
		s.probeTimer = time.AfterFunc(s.probeDelay, func() { s.post(s.requestProbe) })
	}
}

func (s *Supervisor) start(u *unit, session string) {
	spec := u.spec
	if u.log == nil {
		log, err := logfile.Open(spec.LogPath, logfile.DefaultMaxSize)
		if err != nil {
			s.startFailed(u, fmt.Errorf("opening the log: %w", err))
			return
		}
		u.log = log
	}
	lines := logfile.NewLineWriter(u.log, time.Now)

	cmd := exec.Command(s.client.Path, s.client.ScriptArgs(spec.Path)...)
	cmd.Env = append(s.client.ScriptEnv(session), spec.Env...)
	cmd.Dir = spec.Dir
	cmd.Stdout, cmd.Stderr = lines, lines
	cmd.WaitDelay = waitDelay
	// Its own process group keeps a Ctrl-C on the daemon's terminal away from
	// the script, so the daemon decides when it stops. It also lets a stop reach
	// whatever the script started.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		s.startFailed(u, err)
		return
	}

	u.run++
	u.pid = cmd.Process.Pid
	u.startedAt = time.Now()
	u.nextStart = time.Time{}
	if u.restartDue {
		u.restartDue = false
		u.restarts++
	}
	s.setState(u, state.ScriptRunning)
	s.log.Info("script started", "plugin", spec.Plugin, "script", spec.Script, "pid", u.pid)
	note(u.log, "rexd: started, pid %d", u.pid)

	key, run, output := spec.key(), u.run, u.log
	go func() {
		err := cmd.Wait()
		ended := time.Now()
		code := exitCode(cmd)
		lines.Flush() // Wait has returned, so nothing else writes to lines now.
		if code == -1 {
			note(output, "rexd: exited: %v", err)
		} else {
			note(output, "rexd: exited with status %d", code)
		}
		s.post(func() { s.handleExit(key, run, code, err, ended) })
	}()
}

// note writes a line of rexd's own to a script's log. It has its own LineWriter
// because the one for the script's output belongs to os/exec while the script runs.
func note(log *logfile.Writer, format string, args ...any) {
	fmt.Fprintf(logfile.NewLineWriter(log, time.Now), format+"\n", args...)
}

// startFailed handles a script that never got a process.
func (s *Supervisor) startFailed(u *unit, err error) {
	s.log.Error("could not start script", "plugin", u.spec.Plugin, "script", u.spec.Script, "error", err)
	u.lastRan = 0
	u.lastExit = &state.Exit{Code: -1, Error: err.Error(), At: time.Now(), Ran: "0s"}
	s.fail(u)
}

func exitCode(cmd *exec.Cmd) int {
	if cmd.ProcessState != nil {
		return cmd.ProcessState.ExitCode()
	}
	return -1
}

func (s *Supervisor) handleExit(key string, run, code int, waitErr error, ended time.Time) {
	u := s.units[key]
	if u == nil || u.run != run {
		return
	}
	stopTimer(u.killTimer)
	// Whatever the script started goes with it, or each restart would leave more.
	signalGroup(u.pid, syscall.SIGKILL)
	u.pid = 0
	u.lastRan = ended.Sub(u.startedAt)
	s.log.Info("script exited", "plugin", u.spec.Plugin, "script", u.spec.Script, "status", code, "ran", u.lastRan.Round(time.Millisecond))

	switch {
	case u.removing:
		s.drop(u)
		return
	case u.pending != nil:
		spec := *u.pending
		u.pending = nil
		s.reset(u, spec)
		s.requestProbe()
		return
	}

	// Rex's answer decides whether this was a failure. An exit status cannot:
	// a script may exit with the status rex uses for "server gone", and rex can
	// change its statuses.
	u.exitToJudge = true
	u.lastExit = &state.Exit{Code: code, At: ended, Ran: u.lastRan.Round(time.Millisecond).String()}
	var exit *exec.ExitError
	if waitErr != nil && (code == -1 || !errors.As(waitErr, &exit)) {
		u.lastExit.Error = waitErr.Error()
	}
	s.setState(u, state.ScriptWaiting)
	s.requestProbe()
}

// fail schedules a restart for a script that exited while Rex was there.
func (s *Supervisor) fail(u *unit) {
	timing := s.opts.Timing
	now := time.Now()
	if u.lastRan >= timing.ResetAfter {
		u.delay, u.failures = 0, nil
	}
	u.failures = append(u.failures, now)
	u.failures = slices.DeleteFunc(u.failures, func(at time.Time) bool {
		return now.Sub(at) > timing.CrashWindow
	})
	u.delay = nextDelay(u.delay, timing.BackoffMin, timing.BackoffMax)
	next := state.ScriptBackoff
	if len(u.failures) >= timing.CrashLimit {
		next = state.ScriptCrashing
		u.delay = timing.BackoffMax
	}
	u.restartDue = true
	u.nextStart = now.Add(u.delay)
	s.setState(u, next)

	stopTimer(u.timer)
	key, run := u.spec.key(), u.run
	u.timer = time.AfterFunc(u.delay, func() {
		s.post(func() { s.handleBackoffDone(key, run) })
	})
}

func nextDelay(current, lowest, highest time.Duration) time.Duration {
	if current == 0 {
		return lowest
	}
	return min(current*2, highest)
}

func (s *Supervisor) handleBackoffDone(key string, run int) {
	u := s.units[key]
	if u == nil || u.run != run || (u.state != state.ScriptBackoff && u.state != state.ScriptCrashing) {
		return
	}
	s.setState(u, state.ScriptWaiting)
	s.requestProbe()
}

// stop asks a running script to exit, and kills it if it does not.
func (s *Supervisor) stop(u *unit) {
	if u.state == state.ScriptStopping {
		return
	}
	s.setState(u, state.ScriptStopping)
	signalGroup(u.pid, syscall.SIGINT)
	key, run := u.spec.key(), u.run
	u.killTimer = time.AfterFunc(s.opts.Timing.StopTimeout, func() {
		s.post(func() {
			if u := s.units[key]; u != nil && u.run == run && u.pid != 0 {
				s.log.Warn("script did not stop; killing it", "plugin", u.spec.Plugin, "script", u.spec.Script, "pid", u.pid)
				signalGroup(u.pid, syscall.SIGKILL)
			}
		})
	})
}

func signalGroup(pid int, sig syscall.Signal) {
	// The group's ID is the script's process ID. It may have exited already.
	_ = syscall.Kill(-pid, sig)
}

// shutdown stops every script and removes the status file.
func (s *Supervisor) shutdown() {
	s.stopping = true
	stopTimer(s.probeTimer)
	stopTimer(s.reloadTimer)
	for _, key := range s.sortedKeys() {
		u := s.units[key]
		u.removing, u.pending = true, nil
		if u.pid != 0 {
			s.stop(u)
		} else {
			s.drop(u)
		}
	}
	s.flushStatus()

	// Long enough for SIGINT, then SIGKILL, then Wait giving up on the output.
	deadline := time.After(s.opts.Timing.StopTimeout + waitDelay + time.Second)
wait:
	for len(s.units) > 0 {
		select {
		case event := <-s.events:
			event()
		case <-deadline:
			s.log.Error("scripts did not stop in time", "left", len(s.units))
			break wait
		}
	}
	if err := os.Remove(s.opts.StatusPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.log.Warn("could not remove the status file", "error", err)
	}
}

func (s *Supervisor) setState(u *unit, next state.ScriptState) {
	if u.state != next {
		u.state = next
		u.since = time.Now()
	}
	s.dirty = true
}

func (s *Supervisor) sortedKeys() []string {
	keys := make([]string, 0, len(s.units))
	for key := range s.units {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// flushStatus writes the status file if anything changed.
func (s *Supervisor) flushStatus() {
	if !s.dirty {
		return
	}
	s.dirty = false
	status := &state.Status{
		Daemon: state.DaemonStatus{
			PID:       os.Getpid(),
			StartedAt: s.startedAt,
			UpdatedAt: time.Now(),
			Rex:       s.rexState,
			Problem:   s.problem,
		},
		Scripts:      []state.ScriptStatus{},
		PluginErrors: s.pluginErrors,
	}
	for _, key := range s.sortedKeys() {
		u := s.units[key]
		status.Scripts = append(status.Scripts, state.ScriptStatus{
			Plugin:    u.spec.Plugin,
			Script:    u.spec.Script,
			Path:      u.spec.Path,
			State:     u.state,
			PID:       u.pid,
			Since:     u.since,
			Restarts:  u.restarts,
			NextStart: u.nextStart,
			LastExit:  u.lastExit,
		})
	}
	if err := status.Save(s.opts.StatusPath); err != nil {
		s.log.Error("could not write the status file", "error", err)
	}
}

func stopTimer(t *time.Timer) {
	if t != nil {
		t.Stop()
	}
}
