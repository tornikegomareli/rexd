package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"text/tabwriter"
	"time"

	"github.com/tornikegomareli/rexd/internal/daemon"
	"github.com/tornikegomareli/rexd/internal/installation"
	"github.com/tornikegomareli/rexd/internal/paths"
	"github.com/tornikegomareli/rexd/internal/plugins"
	"github.com/tornikegomareli/rexd/internal/rex"
	"github.com/tornikegomareli/rexd/internal/state"
)

// manager builds the plugin manager from the environment.
func (a *app) manager() (*plugins.Manager, error) {
	inst, err := installation.Open(a.stdout)
	if err != nil {
		return nil, err
	}
	return inst.Manager()
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
	inst, err := installation.Open(a.stdout)
	if err != nil {
		return err
	}
	return inst.Setup(ctx, installation.SetupOptions{
		SetupOptions: plugins.SetupOptions{RexConfig: *rexConfig, Server: *server},
		NoService:    *noService,
	})
}

func runTeardown(ctx context.Context, a *app, args []string) error {
	fs := a.flags("teardown")
	purge := fs.Bool("purge", false, "also delete installed plugins, state and logs")
	if err := a.parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("takes no arguments")
	}
	inst, err := installation.Open(a.stdout)
	if err != nil {
		return err
	}
	return inst.Teardown(ctx, *purge)
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
		if r.Changed && r.From == r.To {
			fmt.Fprintf(a.stdout, "Updated %s: new code, still %s\n", r.ID, r.To)
		} else if r.Changed {
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

func runSync(ctx context.Context, a *app, args []string) error {
	fs := a.flags("sync")
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
	results, err := m.Sync(ctx)
	for _, r := range results {
		fmt.Fprintf(a.stdout, "%s: %s\n", r.ID, r.Note)
	}
	return err
}

func runSet(ctx context.Context, a *app, args []string) error {
	fs := a.flags("set")
	if err := a.parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return usagef("needs one plugin")
	}
	id := fs.Arg(0)
	if fs.NArg() == 1 {
		return printOptions(a, id)
	}
	changes := map[string]any{}
	for _, arg := range fs.Args()[1:] {
		key, value, err := plugins.ParseOption(arg)
		if err != nil {
			return usageError{err.Error()}
		}
		changes[key] = value
	}
	m, err := a.manager()
	if err != nil {
		return err
	}
	if err := m.SetOptions(ctx, id, changes); err != nil {
		return err
	}
	return printOptions(a, id)
}

func printOptions(a *app, id string) error {
	dirs, err := paths.Default()
	if err != nil {
		return err
	}
	current, err := state.Load(dirs.StateFile())
	if err != nil {
		return err
	}
	p, ok := current.Plugins[id]
	if !ok {
		return fmt.Errorf("%s is not installed", id)
	}
	if len(p.Options) == 0 {
		fmt.Fprintf(a.stdout, "%s has no options set.\n", id)
		return nil
	}
	for _, key := range slices.Sorted(maps.Keys(p.Options)) {
		fmt.Fprintf(a.stdout, "%s=%v\n", key, p.Options[key])
	}
	return nil
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
	Daemon daemonReport   `json:"daemon"`
	Rex    state.RexState `json:"rex"`
	// RexConfig is the init.lua rexd is connected to. It is empty before rexd setup.
	RexConfig string         `json:"rex_config"`
	Plugins   []pluginReport `json:"plugins"`
}

type daemonReport struct {
	Running bool `json:"running"`
	PID     int  `json:"pid,omitempty"`
	// Problem says why a running daemon cannot do its work right now.
	Problem string `json:"problem,omitempty"`
}

type pluginReport struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Kind    string `json:"kind"`
	// HasConfig says whether the plugin has config code in Rex.
	HasConfig bool `json:"has_config"`
	// ConfigBroken says Rex's checker rejects that config code.
	ConfigBroken bool `json:"config_broken,omitempty"`
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
	report := &statusReport{RexConfig: current.Settings.RexConfig, Plugins: []pluginReport{}}
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
		// Only a script makes the daemon watch Rex. Without one, its answer is
		// old or missing.
		if len(status.Scripts) > 0 {
			report.Rex = status.Daemon.Rex
		}
		report.Daemon.Problem = status.Daemon.Problem
	}

	for _, p := range current.SortedPlugins() {
		plugin := pluginReport{ID: p.ID, Version: p.Version, Kind: string(p.Kind), Scripts: []state.ScriptStatus{}}
		plugin.HasConfig, err = m.CheckConfigCode(ctx, current, p)
		if err != nil {
			plugin.ConfigBroken = true
			plugin.Problem = "config code: " + err.Error()
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
		if report.Rex != "" {
			fmt.Fprintf(a.stdout, "Rex:    %s\n", describeRex(report.Rex))
		}
		if report.Daemon.Problem != "" {
			fmt.Fprintf(a.stdout, "Problem: %s\n", report.Daemon.Problem)
		}
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
		switch {
		case p.ConfigBroken:
			config = "error"
		case p.HasConfig:
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
	return string(rexState)
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
		reason := ""
		if s.LastExit != nil {
			reason = fmt.Sprintf(" with status %d", s.LastExit.Code)
			if s.LastExit.Code == -1 && s.LastExit.Error != "" {
				reason = ": " + s.LastExit.Error
			}
		}
		return fmt.Sprintf("%s%s, restarting in %s", word, reason, time.Until(s.NextStart).Round(time.Second))
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
