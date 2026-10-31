// Package installation is one rexd on this machine: its folders, its daemon, and
// the LaunchAgent that keeps the daemon running.
//
// The folders decide everything else. launchd has one list of agents for the whole
// account, so an installation with other folders needs an agent of its own, or it
// would replace and remove the agent of the normal installation.
package installation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tornikegomareli/rexd/internal/daemon"
	"github.com/tornikegomareli/rexd/internal/paths"
	"github.com/tornikegomareli/rexd/internal/plugins"
	"github.com/tornikegomareli/rexd/internal/rex"
	"github.com/tornikegomareli/rexd/internal/service"
)

const (
	// daemonStartWait is how long setup waits to see the daemon running.
	daemonStartWait = 5 * time.Second
	// daemonStopWait covers the daemon stopping every script.
	daemonStopWait = 20 * time.Second
)

// Installation is the rexd that the environment points at.
type Installation struct {
	Dirs paths.Dirs
	Out  io.Writer
}

// Open finds the installation from the environment.
func Open(out io.Writer) (*Installation, error) {
	dirs, err := paths.Default()
	if err != nil {
		return nil, err
	}
	return &Installation{Dirs: dirs, Out: out}, nil
}

// Manager returns the plugin manager of this installation. It needs Rex.
func (i *Installation) Manager() (*plugins.Manager, error) {
	path, err := rex.Find()
	if err != nil {
		return nil, err
	}
	return &plugins.Manager{Dirs: i.Dirs, Rex: rex.Client{Path: path}, Out: i.Out}, nil
}

// Label names the LaunchAgent of the installation with these folders. It asks the
// account for its home, and not $HOME, because launchd serves the account.
func Label(dirs paths.Dirs) (string, error) {
	account, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("finding the home folder: %w", err)
	}
	return labelFor(dirs, account.HomeDir), nil
}

func labelFor(dirs paths.Dirs, home string) string {
	if dirs == paths.Under(home) {
		return service.Label
	}
	sum := sha256.Sum256([]byte(dirs.Data + "\x00" + dirs.State))
	return service.Label + "." + hex.EncodeToString(sum[:6])
}

// SetupOptions says how to connect this installation to Rex.
type SetupOptions struct {
	plugins.SetupOptions
	// NoService leaves the daemon to the user.
	NoService bool
}

// Setup connects rexd to Rex and starts the daemon. It is safe to run again.
func (i *Installation) Setup(ctx context.Context, opts SetupOptions) error {
	m, err := i.Manager()
	if err != nil {
		return err
	}
	settings, err := m.Setup(ctx, opts.SetupOptions)
	if err != nil {
		return err
	}
	i.printf("Connected to %s", settings.RexConfig)

	if opts.NoService {
		return i.reportDaemon()
	}
	if !service.Supported() {
		i.printf("This system has no LaunchAgent. Run \"rexd daemon\" under your init system to keep event scripts running.")
		return nil
	}
	spec, err := i.serviceSpec()
	if err != nil {
		return err
	}
	if err := service.Install(ctx, spec); err != nil {
		return fmt.Errorf("starting the daemon: %w", err)
	}
	if !i.waitForDaemon(ctx) {
		return fmt.Errorf("the LaunchAgent is installed, but the daemon did not start; see %s", spec.LogPath)
	}
	i.printf("The daemon is running and starts at login.")
	return nil
}

func (i *Installation) reportDaemon() error {
	notified, err := daemon.Notify(i.Dirs)
	if err != nil {
		return err
	}
	if notified {
		i.printf("The daemon is running.")
		return nil
	}
	i.printf("Run \"rexd daemon\" to keep event scripts running.")
	return nil
}

// serviceSpec describes the LaunchAgent. launchd starts programs with almost no
// environment, so it keeps the PATH of this shell for plugins to find their tools.
func (i *Installation) serviceSpec() (service.Spec, error) {
	label, err := Label(i.Dirs)
	if err != nil {
		return service.Spec{}, err
	}
	// Not resolved: Homebrew's link stays valid after an upgrade, and what it points
	// to does not.
	exe, err := os.Executable()
	if err != nil {
		return service.Spec{}, err
	}
	env := map[string]string{}
	for _, name := range []string{"PATH", "REXD_REX", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		if value := os.Getenv(name); value != "" {
			env[name] = value
		}
	}
	if path, ok := env["PATH"]; ok {
		env["PATH"] = withoutVersionedRex(path)
	}
	return service.Spec{
		Label:   label,
		Program: []string{exe, "daemon"},
		Env:     env,
		LogPath: filepath.Join(i.Dirs.Logs(), "launchd.log"),
	}, nil
}

// withoutVersionedRex drops the folder a Rex terminal adds to PATH. It names one
// version of Rex, which a later update replaces. Without it, rex.Find uses the
// path that always points at the current version.
func withoutVersionedRex(path string) string {
	folders := filepath.SplitList(path)
	folders = slices.DeleteFunc(folders, func(folder string) bool {
		return strings.Contains(folder, "RexServer/Versions/")
	})
	return strings.Join(folders, string(os.PathListSeparator))
}

func (i *Installation) waitForDaemon(ctx context.Context) bool {
	deadline := time.Now().Add(daemonStartWait)
	for time.Now().Before(deadline) {
		if _, running, err := daemon.Running(i.Dirs); err == nil && running {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
	return false
}

// Teardown undoes Setup, and with purge it also deletes plugins, state and logs. It
// works after Rex is gone: only telling Rex to reload needs it.
func (i *Installation) Teardown(ctx context.Context, purge bool) error {
	// The LaunchAgent goes first. It would start a stopped daemon again.
	if service.Supported() {
		label, err := Label(i.Dirs)
		if err != nil {
			return err
		}
		if err := service.Uninstall(ctx, label); err != nil {
			return err
		}
	}
	if _, err := daemon.Stop(ctx, i.Dirs, daemonStopWait); err != nil {
		return err
	}
	m, err := i.Manager()
	if errors.Is(err, rex.ErrNotFound) {
		i.printf("Rex is not installed, so there is no config to reload.")
		m = &plugins.Manager{Dirs: i.Dirs, Out: i.Out}
	} else if err != nil {
		return err
	}
	if err := m.Teardown(ctx, plugins.TeardownOptions{Purge: purge}); err != nil {
		return err
	}
	if purge {
		i.printf("rexd is disconnected from Rex, and its plugins and state are deleted.")
		return nil
	}
	i.printf("rexd is disconnected from Rex. Plugins stay in %s, and \"rexd setup\" brings them back.", i.Dirs.Plugins())
	return nil
}

func (i *Installation) printf(format string, args ...any) {
	fmt.Fprintf(i.Out, format+"\n", args...)
}
