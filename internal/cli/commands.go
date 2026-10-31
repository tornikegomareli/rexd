package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tornikegomareli/rexd/internal/daemon"
	"github.com/tornikegomareli/rexd/internal/paths"
	"github.com/tornikegomareli/rexd/internal/plugins"
	"github.com/tornikegomareli/rexd/internal/rex"
	"github.com/tornikegomareli/rexd/internal/rexconfig"
	"github.com/tornikegomareli/rexd/internal/service"
	"github.com/tornikegomareli/rexd/internal/state"
)

const (
	// daemonStartWait is how long setup waits to see the daemon running.
	daemonStartWait = 5 * time.Second
	// daemonStopWait covers the daemon stopping every script.
	daemonStopWait = 20 * time.Second
)

// manager builds the plugin manager from the environment.
func (a *app) manager() (*plugins.Manager, error) {
	dirs, err := paths.Default()
	if err != nil {
		return nil, err
	}
	path, err := rex.Find()
	if err != nil {
		return nil, err
	}
	return &plugins.Manager{Dirs: dirs, Rex: rex.Client{Path: path}, Out: a.stdout}, nil
}

func runSetup(ctx context.Context, a *app, args []string) error {
	fs := a.flags("setup")
	rexConfig := fs.String("rex-config", "", "`path` of Rex's init.lua (default: ask the running server)")
	server := fs.String("server", "", "Rex server `address`, as for rex -S (default: Rex's default server)")
	noService := fs.Bool("no-service", false, "do not install a LaunchAgent; run \"rexd daemon\" yourself")
	if err := a.parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("takes no arguments")
	}

	m, err := a.manager()
	if err != nil {
		return err
	}
	settings, err := m.Setup(ctx, plugins.SetupOptions{RexConfig: *rexConfig, Server: *server})
	if err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "Connected to %s\n", settings.RexConfig)

	if *noService {
		return a.reportDaemonWithoutService(m.Dirs)
	}
	spec, err := a.serviceSpec(m.Dirs)
	if err != nil {
		return err
	}
	err = service.Install(ctx, spec)
	if errors.Is(err, service.ErrUnsupported) {
		fmt.Fprintf(a.stdout, "This system has no LaunchAgent. Run \"rexd daemon\" under your init system to keep event scripts running.\n")
		return nil
	}
	if err != nil {
		return fmt.Errorf("starting the daemon: %w", err)
	}
	if !waitForDaemon(ctx, m.Dirs) {
		return fmt.Errorf("the LaunchAgent is installed, but the daemon did not start; see %s", spec.LogPath)
	}
	fmt.Fprintf(a.stdout, "The daemon is running and starts at login.\n")
	return nil
}

func (a *app) reportDaemonWithoutService(dirs paths.Dirs) error {
	notified, err := daemon.Notify(dirs)
	if err != nil {
		return err
	}
	if notified {
		fmt.Fprintf(a.stdout, "The daemon is running.\n")
		return nil
	}
	fmt.Fprintf(a.stdout, "Run \"rexd daemon\" to keep event scripts running.\n")
	return nil
}

// serviceSpec describes the LaunchAgent. launchd starts programs with almost no
// environment, so it keeps the PATH of this shell for plugins to find their tools.
func (a *app) serviceSpec(dirs paths.Dirs) (service.Spec, error) {
	exe := a.executable
	if exe == "" {
		var err error
		exe, err = os.Executable()
		if err != nil {
			return service.Spec{}, err
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
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
		Label:   a.serviceLabel,
		Program: []string{exe, "daemon"},
		Env:     env,
		LogPath: filepath.Join(dirs.Logs(), "launchd.log"),
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

func waitForDaemon(ctx context.Context, dirs paths.Dirs) bool {
	deadline := time.Now().Add(daemonStartWait)
	for time.Now().Before(deadline) {
		if _, running, err := daemon.Running(dirs); err == nil && running {
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

func runTeardown(ctx context.Context, a *app, args []string) error {
	fs := a.flags("teardown")
	purge := fs.Bool("purge", false, "also delete installed plugins, state and logs")
	noService := fs.Bool("no-service", false, "leave the LaunchAgent alone")
	if err := a.parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("takes no arguments")
	}
	m, err := a.manager()
	if err != nil {
		return err
	}
	if !*noService {
		if err := service.Uninstall(ctx, a.serviceLabel); err != nil {
			return err
		}
	}
	if _, err := daemon.Stop(ctx, m.Dirs, daemonStopWait); err != nil {
		return err
	}
	if err := m.Teardown(ctx, plugins.TeardownOptions{Purge: *purge}); err != nil {
		return err
	}
	if *purge {
		fmt.Fprintf(a.stdout, "rexd is disconnected from Rex, and its plugins and state are deleted.\n")
		return nil
	}
	fmt.Fprintf(a.stdout, "rexd is disconnected from Rex. Plugins stay in %s, and \"rexd setup\" brings them back.\n", m.Dirs.Plugins())
	return nil
}

func runInstall(ctx context.Context, a *app, args []string) error {
	fs := a.flags("install")
	link := fs.Bool("link", false, "link a local folder instead of copying it, for plugin development")
	ref := fs.String("ref", "", "git branch, tag or commit to check out, as `ref`")
	if err := a.parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return usagef("needs one source")
	}
	src, err := plugins.ParseSource(fs.Arg(0), *ref, *link)
	if err != nil {
		return usageError{err.Error()}
	}
	m, err := a.manager()
	if err != nil {
		return err
	}
	plugin, err := m.Install(ctx, src)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "Installed %s %s\n", plugin.ID, plugin.Version)
	return nil
}

func runRemove(ctx context.Context, a *app, args []string) error {
	fs := a.flags("remove")
	if err := a.parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return usagef("needs one plugin")
	}
	m, err := a.manager()
	if err != nil {
		return err
	}
	if err := m.Remove(ctx, fs.Arg(0)); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "Removed %s\n", fs.Arg(0))
	return nil
}

func runUpdate(ctx context.Context, a *app, args []string) error {
	fs := a.flags("update")
	if err := a.parse(fs, args); err != nil {
		return err
	}
	m, err := a.manager()
	if err != nil {
		return err
	}
	results, err := m.Update(ctx, fs.Args())
	for _, r := range results {
		if r.Changed {
			fmt.Fprintf(a.stdout, "Updated %s: %s -> %s\n", r.ID, r.From, r.To)
		} else {
			fmt.Fprintf(a.stdout, "%s: %s\n", r.ID, r.Note)
		}
	}
	if err == nil && len(results) == 0 {
		fmt.Fprintf(a.stdout, "No plugins are installed.\n")
	}
	return err
}

func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func runList(_ context.Context, a *app, args []string) error {
	fs := a.flags("list")
	asJSON := fs.Bool("json", false, "print as JSON")
	if err := a.parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("takes no arguments")
	}
	dirs, err := paths.Default()
	if err != nil {
		return err
	}
	current, err := state.Load(dirs.StateFile())
	if err != nil {
		return err
	}
	installed := current.SortedPlugins()
	if *asJSON {
		return a.printJSON(installed)
	}
	if len(installed) == 0 {
		fmt.Fprintf(a.stdout, "No plugins are installed. Install one with: rexd install owner/repo\n")
		return nil
	}
	table := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "PLUGIN\tVERSION\tKIND\tSOURCE")
	for _, p := range installed {
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", p.ID, p.Version, p.Kind, p.Source)
	}
	return table.Flush()
}

// statusReport is what rexd status knows. It is also the --json format.
type statusReport struct {
	Daemon  daemonReport   `json:"daemon"`
	Rex     state.RexState `json:"rex"`
	Plugins []pluginReport `json:"plugins"`
}

type daemonReport struct {
	Running bool `json:"running"`
	PID     int  `json:"pid,omitempty"`
}

type pluginReport struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Kind    string `json:"kind"`
	// HasConfig says whether the plugin has config code in Rex.
	HasConfig bool `json:"has_config"`
	// Problem says why the plugin does not work, if something is wrong with it.
	Problem string               `json:"problem,omitempty"`
	Scripts []state.ScriptStatus `json:"scripts"`
}

func runStatus(ctx context.Context, a *app, args []string) error {
	fs := a.flags("status")
	asJSON := fs.Bool("json", false, "print as JSON")
	if err := a.parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("takes no arguments")
	}
	report, err := a.gatherStatus(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		return a.printJSON(report)
	}
	a.printStatus(report)
	return nil
}

func (a *app) gatherStatus(ctx context.Context) (*statusReport, error) {
	m, err := a.manager()
	if err != nil {
		return nil, err
	}
	current, err := state.Load(m.Dirs.StateFile())
	if err != nil {
		return nil, err
	}
	report := &statusReport{Plugins: []pluginReport{}}
	pid, running, err := daemon.Running(m.Dirs)
	if err != nil {
		return nil, err
	}
	report.Daemon = daemonReport{Running: running, PID: pid}

	// The status file only means something while its daemon is alive.
	var status *state.Status
	if running {
		status, err = state.LoadStatus(m.Dirs.StatusFile())
		if err != nil {
			return nil, err
		}
	}
	if status != nil {
		report.Rex = status.Daemon.Rex
	}

	client := m.Rex
	client.Server = current.Settings.Server
	layout := rexconfig.Layout{Init: current.Settings.RexConfig}
	for _, p := range current.SortedPlugins() {
		plugin := pluginReport{ID: p.ID, Version: p.Version, Kind: string(p.Kind), Scripts: []state.ScriptStatus{}}
		if current.Settings.RexConfig != "" {
			if _, err := os.Stat(layout.Module(p.ID)); err == nil {
				plugin.HasConfig = true
				entry := rexconfig.Entry{ID: p.ID, Version: p.Version, Dir: m.Dirs.Plugin(p.ID)}
				if err := layout.Check(ctx, client, entry); err != nil {
					plugin.Problem = "config code: " + err.Error()
				}
			}
		}
		if status != nil {
			for _, script := range status.Scripts {
				if script.Plugin == p.ID {
					plugin.Scripts = append(plugin.Scripts, script)
				}
			}
			if problem, ok := status.PluginErrors[p.ID]; ok {
				plugin.Problem = problem
			}
		}
		report.Plugins = append(report.Plugins, plugin)
	}
	return report, nil
}

func (a *app) printStatus(report *statusReport) {
	if report.Daemon.Running {
		fmt.Fprintf(a.stdout, "Daemon: running, pid %d\n", report.Daemon.PID)
		fmt.Fprintf(a.stdout, "Rex:    %s\n", describeRex(report.Rex))
	} else {
		fmt.Fprintf(a.stdout, "Daemon: not running. Run \"rexd setup\" to start it.\n")
	}
	if len(report.Plugins) == 0 {
		fmt.Fprintf(a.stdout, "\nNo plugins are installed. Install one with: rexd install owner/repo\n")
		return
	}

	fmt.Fprintln(a.stdout)
	table := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "PLUGIN\tVERSION\tCONFIG\tSCRIPT\tSTATE\tRESTARTS")
	for _, p := range report.Plugins {
		config := "-"
		if p.HasConfig {
			config = "loaded"
		}
		if len(p.Scripts) == 0 {
			fmt.Fprintf(table, "%s\t%s\t%s\t-\t-\t-\n", p.ID, p.Version, config)
			continue
		}
		for _, s := range p.Scripts {
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%d\n", p.ID, p.Version, config, s.Script, describeScript(s, report.Rex), s.Restarts)
		}
	}
	table.Flush()

	for _, p := range report.Plugins {
		if p.Problem != "" {
			fmt.Fprintf(a.stdout, "\n%s: %s\n", p.ID, p.Problem)
		}
	}
}

func describeRex(rexState state.RexState) string {
	switch rexState {
	case state.RexUp:
		return "running"
	case state.RexNoSessions:
		return "running, with no sessions yet"
	case state.RexDown:
		return "not running"
	}
	return "not checked yet"
}

func describeScript(s state.ScriptStatus, rexState state.RexState) string {
	switch s.State {
	case state.ScriptRunning:
		return fmt.Sprintf("running, pid %d", s.PID)
	case state.ScriptStopping:
		return "stopping"
	case state.ScriptWaiting:
		switch rexState {
		case state.RexDown:
			return "waiting for Rex"
		case state.RexNoSessions:
			return "waiting for a session"
		}
		return "starting"
	case state.ScriptBackoff, state.ScriptCrashing:
		word := "exited"
		if s.State == state.ScriptCrashing {
			word = "crashing"
		}
		code := ""
		if s.LastExit != nil {
			code = fmt.Sprintf(" with status %d", s.LastExit.Code)
		}
		return fmt.Sprintf("%s%s, restarting in %s", word, code, time.Until(s.NextStart).Round(time.Second))
	}
	return string(s.State)
}

func runDaemon(ctx context.Context, a *app, args []string) error {
	fs := a.flags("daemon")
	if err := a.parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("takes no arguments")
	}
	dirs, err := paths.Default()
	if err != nil {
		return err
	}
	opts := daemon.Options{Dirs: dirs, FindRex: rex.Find}
	if info, err := os.Stderr.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
		opts.LogOutput = a.stderr
	}
	return daemon.Run(ctx, opts)
}
