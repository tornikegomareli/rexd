// Package daemon runs the supervisor as a single background process and lets
// other commands talk to it.
//
// The daemon and the command line share files instead of a socket. Commands
// change state.json and send the daemon SIGHUP. The daemon reads the file and
// reports what is running in status.json. So everything rexd knows survives a
// restart of either side.
package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tornikegomareli/rexd/internal/fsutil"
	"github.com/tornikegomareli/rexd/internal/lockfile"
	"github.com/tornikegomareli/rexd/internal/logfile"
	"github.com/tornikegomareli/rexd/internal/manifest"
	"github.com/tornikegomareli/rexd/internal/paths"
	"github.com/tornikegomareli/rexd/internal/rex"
	"github.com/tornikegomareli/rexd/internal/state"
	"github.com/tornikegomareli/rexd/internal/supervisor"
)

const (
	lockAttempts   = 10
	lockRetryDelay = 100 * time.Millisecond
)

// ErrAlreadyRunning means another daemon holds the lock.
var ErrAlreadyRunning = errors.New("the rexd daemon is already running")

// Options configures Run.
type Options struct {
	Dirs paths.Dirs
	// FindRex locates the rex binary. It runs on every reload, so a daemon that
	// started before Rex was installed finds it later.
	FindRex func() (string, error)
	// LogOutput also receives the daemon's log, such as a terminal. It may be nil.
	LogOutput io.Writer
	// Timing overrides the supervisor's delays when set.
	Timing *supervisor.Timing
}

// Run holds the daemon lock and supervises scripts until ctx ends or a signal
// says to stop. SIGHUP makes it read the state again.
func Run(ctx context.Context, opts Options) error {
	// Signals come first. A command finds the daemon through its PID file, and
	// without a handler its SIGHUP would kill the daemon.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	hangups := make(chan os.Signal, 1)
	signal.Notify(hangups, syscall.SIGHUP)
	defer signal.Stop(hangups)

	dirs := opts.Dirs
	lock, err := acquireDaemonLock(dirs)
	if err != nil {
		return err
	}
	defer lock.Release()

	pid := []byte(strconv.Itoa(os.Getpid()) + "\n")
	if err := fsutil.WriteFileAtomic(dirs.DaemonPID(), pid, 0o644); err != nil {
		return err
	}
	defer os.Remove(dirs.DaemonPID())

	logFile, err := logfile.Open(dirs.DaemonLog(), logfile.DefaultMaxSize)
	if err != nil {
		return err
	}
	defer logFile.Close()
	var logOutput io.Writer = logFile
	if opts.LogOutput != nil {
		logOutput = io.MultiWriter(logFile, opts.LogOutput)
	}
	log := slog.New(slog.NewTextHandler(logOutput, nil))
	log.Info("daemon started", "pid", os.Getpid())
	defer log.Info("daemon stopped")

	reload := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case <-hangups:
				log.Info("reloading")
				select {
				case reload <- struct{}{}:
				default: // A reload is already waiting, and it will read the latest state.
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	timing := supervisor.DefaultTiming()
	if opts.Timing != nil {
		timing = *opts.Timing
	}
	return supervisor.New(supervisor.Options{
		Load:       func() (supervisor.Desired, error) { return LoadDesired(dirs, opts.FindRex) },
		StatusPath: dirs.StatusFile(),
		Logger:     log,
		Timing:     timing,
	}).Run(ctx, reload)
}

// acquireDaemonLock takes the lock, retrying for a moment. Running holds the lock
// for an instant when it checks for a daemon, and that must not look like one.
func acquireDaemonLock(dirs paths.Dirs) (*lockfile.Lock, error) {
	for attempt := 0; ; attempt++ {
		lock, err := lockfile.TryAcquire(dirs.DaemonLock())
		if !errors.Is(err, lockfile.ErrHeld) {
			return lock, err
		}
		if attempt == lockAttempts {
			return nil, ErrAlreadyRunning
		}
		time.Sleep(lockRetryDelay)
	}
}

// LoadDesired works out which scripts should run from the state and each
// plugin's manifest. A plugin it cannot read is reported and skipped, so one bad
// plugin does not stop the others.
func LoadDesired(dirs paths.Dirs, findRex func() (string, error)) (supervisor.Desired, error) {
	current, err := state.Load(dirs.StateFile())
	if err != nil {
		return supervisor.Desired{}, err
	}
	path, err := findRex()
	if err != nil {
		return supervisor.Desired{}, err
	}
	desired := supervisor.Desired{
		Client:       rex.Client{Path: path, Server: current.Settings.Server},
		PluginErrors: map[string]string{},
	}
	for _, plugin := range current.SortedPlugins() {
		dir := dirs.Plugin(plugin.ID)
		m, _, err := manifest.Load(dir)
		if err != nil {
			desired.PluginErrors[plugin.ID] = err.Error()
			continue
		}
		for _, script := range m.Scripts {
			desired.Specs = append(desired.Specs, supervisor.Spec{
				Plugin: plugin.ID,
				Script: script.Name,
				Path:   filepath.Join(dir, filepath.Clean(script.Path)),
				Dir:    dir,
				Env: []string{
					"REXD_PLUGIN_ID=" + plugin.ID,
					"REXD_PLUGIN_VERSION=" + plugin.Version,
					"REXD_PLUGIN_DIR=" + dir,
					"REXD_SCRIPT=" + script.Name,
				},
				LogPath:     dirs.ScriptLog(plugin.ID, script.Name),
				Fingerprint: fingerprint(current.Settings.Server, plugin, script),
			})
		}
	}
	return desired, nil
}

// fingerprint changes when a script must restart: on every install and update of
// its plugin, or when rexd is pointed at another server.
func fingerprint(server string, plugin *state.Plugin, script manifest.Script) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		server, plugin.Version, strconv.Itoa(plugin.Generation), script.Path,
	}, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// Running reports whether a daemon holds the lock, and its process ID if known.
func Running(dirs paths.Dirs) (pid int, running bool, err error) {
	lock, err := lockfile.TryAcquire(dirs.DaemonLock())
	switch {
	case errors.Is(err, lockfile.ErrHeld):
		data, readErr := os.ReadFile(dirs.DaemonPID())
		if readErr != nil {
			return 0, true, nil // It is between taking the lock and writing the file.
		}
		// The daemon writes the file atomically, so only a hand edit can fail here.
		pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		return pid, true, nil
	case err != nil:
		return 0, false, err
	}
	return 0, false, lock.Release()
}

// Stop asks a running daemon to exit and waits until it has. It reports whether a
// daemon was running.
func Stop(ctx context.Context, dirs paths.Dirs, timeout time.Duration) (bool, error) {
	pid, running, err := Running(dirs)
	if err != nil || !running {
		return false, err
	}
	if pid != 0 {
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			return true, fmt.Errorf("signaling the daemon: %w", err)
		}
	}
	deadline := time.Now().Add(timeout)
	for {
		_, running, err := Running(dirs)
		if err != nil {
			return true, err
		}
		if !running {
			return true, nil
		}
		if time.Now().After(deadline) {
			return true, fmt.Errorf("the daemon did not stop within %s", timeout)
		}
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Notify tells a running daemon to read the state again. It reports whether a
// daemon was there to tell.
func Notify(dirs paths.Dirs) (bool, error) {
	pid, running, err := Running(dirs)
	if err != nil || !running {
		return false, err
	}
	if pid == 0 {
		return true, nil // It has not started yet, and it reads the state when it does.
	}
	if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return false, nil // It exited a moment ago.
		}
		return false, fmt.Errorf("signaling the daemon: %w", err)
	}
	return true, nil
}
