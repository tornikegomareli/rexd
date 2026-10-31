// Package cli implements the rexd command line.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/tornikegomareli/rexd/internal/service"
)

// Options carries what the binary knows about itself and where it writes.
type Options struct {
	// Version is set at build time. Empty means the module version, if any.
	Version string
	Stdout  io.Writer
	Stderr  io.Writer
	// ServiceLabel names the LaunchAgent. Empty means the normal one. Tests set
	// it, so they can never touch a real installation.
	ServiceLabel string
}

type app struct {
	stdout       io.Writer
	stderr       io.Writer
	version      string
	serviceLabel string
}

type command struct {
	name    string
	args    string
	summary string
	help    string
	run     func(ctx context.Context, a *app, args []string) error
}

// usageError is a mistake in how rexd was called. It exits with status 2.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return usageError{fmt.Sprintf(format, args...)}
}

// Main runs rexd with args and returns the process exit status.
func Main(args []string, opts Options) int {
	a := &app{stdout: opts.Stdout, stderr: opts.Stderr, version: opts.Version, serviceLabel: opts.ServiceLabel}
	if a.serviceLabel == "" {
		a.serviceLabel = service.Label
	}
	if a.stdout == nil {
		a.stdout = os.Stdout
	}
	if a.stderr == nil {
		a.stderr = os.Stderr
	}
	if a.version == "" {
		a.version = moduleVersion()
	}

	if len(args) == 0 {
		a.printUsage(a.stderr)
		return 2
	}
	name, rest := args[0], args[1:]
	if name == "-h" || name == "--help" {
		name = "help"
	}
	if name == "--version" {
		name = "version"
	}
	cmd := a.find(name)
	if cmd == nil {
		fmt.Fprintf(a.stderr, "rexd: unknown command %q\nRun \"rexd help\" for the list of commands.\n", name)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := cmd.run(ctx, a, rest)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		return 0
	case errors.As(err, new(usageError)):
		fmt.Fprintf(a.stderr, "rexd %s: %v\nUsage: rexd %s %s\n", cmd.name, err, cmd.name, cmd.args)
		return 2
	default:
		fmt.Fprintf(a.stderr, "rexd: %v\n", err)
		return 1
	}
}

func (a *app) commands() []command {
	return []command{
		{
			name: "setup", args: "[--rex-config PATH] [--server ADDRESS] [--no-service]",
			summary: "Connect rexd to Rex and start the daemon",
			help: `
Adds require("rexd") to the top of Rex's init.lua, writes the generated rexd.lua
beside it, and installs a LaunchAgent that keeps the daemon running. It is safe
to run again, and it repairs whatever is missing.`,
			run: runSetup,
		},
		{
			name: "install", args: "SOURCE [--link] [--ref REF]",
			summary: "Install a plugin",
			help: `
SOURCE is one of:

  owner/repo          a GitHub repository
  owner/repo@ref      a GitHub repository at a branch, tag or commit
  https://host/a.git  any git URL
  ./path              a local folder, which is copied

Rex's own checker loads the plugin's config code before anything is kept. A
plugin's build steps run on install, so install plugins you trust.`,
			run: runInstall,
		},
		{name: "remove", args: "PLUGIN", summary: "Remove a plugin", run: runRemove},
		{
			name: "update", args: "[PLUGIN...]",
			summary: "Update plugins, all of them if none are named",
			help: `
A new version is built beside the old one and checked by Rex before it replaces
the old one. A plugin installed at a tag or commit stays there.`,
			run: runUpdate,
		},
		{name: "list", args: "[--json]", summary: "List installed plugins", run: runList},
		{name: "status", args: "[--json]", summary: "Show the daemon, plugins and event scripts", run: runStatus},
		{
			name: "logs", args: "[PLUGIN] [--script NAME] [--daemon] [-n LINES] [-f]",
			summary: "Print the output of a plugin's event scripts",
			run:     runLogs,
		},
		{
			name: "teardown", args: "[--purge] [--no-service]",
			summary: "Disconnect rexd from Rex and stop the daemon",
			help: `
Removes the line from init.lua, the generated files and the LaunchAgent. Plugins
stay installed, and the next "rexd setup" brings them back. With --purge they are
deleted too, along with rexd's state and logs.`,
			run: runTeardown,
		},
		{
			name: "daemon", args: "",
			summary: "Run the daemon in the foreground",
			help: `
"rexd setup" arranges for this to run on macOS. On other systems, run it under
your init system, such as a systemd user service.`,
			run: runDaemon,
		},
		{name: "help", args: "[command]", summary: "Show help for rexd or one of its commands", run: runHelp},
		{name: "version", args: "", summary: "Print the version", run: runVersion},
	}
}

func (a *app) find(name string) *command {
	for _, c := range a.commands() {
		if c.name == name {
			return &c
		}
	}
	return nil
}

func (a *app) printUsage(w io.Writer) {
	fmt.Fprint(w, "rexd installs plugins for Rex and keeps their event scripts running.\n\n")
	fmt.Fprint(w, "Usage:\n  rexd <command> [arguments]\n\nCommands:\n")
	for _, c := range a.commands() {
		fmt.Fprintf(w, "  %-8s  %s\n", c.name, c.summary)
	}
	fmt.Fprint(w, "\nRun \"rexd help <command>\" for more about a command.\n")
}

// flags returns a flag set for cmd that reports errors instead of exiting.
func (a *app) flags(cmd string) *flag.FlagSet {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parse parses args into fs and turns flag errors into usage errors. Flags may
// come after positional arguments, as in "rexd install ./plugin --link", which
// the flag package alone does not allow.
func (a *app) parse(fs *flag.FlagSet, args []string) error {
	var positional []string
	for {
		err := fs.Parse(args)
		if errors.Is(err, flag.ErrHelp) {
			a.printCommandHelp(a.stdout, a.find(fs.Name()), fs)
			return flag.ErrHelp
		}
		if err != nil {
			return usageError{err.Error()}
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
	return fs.Parse(append([]string{"--"}, positional...))
}

func (a *app) printCommandHelp(w io.Writer, c *command, fs *flag.FlagSet) {
	fmt.Fprintf(w, "Usage: rexd %s %s\n\n%s\n", c.name, c.args, c.summary)
	if c.help != "" {
		fmt.Fprintf(w, "\n%s\n", strings.TrimSpace(c.help))
	}
	if fs == nil {
		return
	}
	var flags strings.Builder
	fs.SetOutput(&flags)
	fs.PrintDefaults()
	fs.SetOutput(io.Discard)
	if flags.Len() > 0 {
		fmt.Fprintf(w, "\nFlags:\n%s", flags.String())
	}
}

func runHelp(ctx context.Context, a *app, args []string) error {
	switch len(args) {
	case 0:
		a.printUsage(a.stdout)
		return nil
	case 1:
		if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
			a.printUsage(a.stdout)
			return nil
		}
		c := a.find(args[0])
		if c == nil {
			return usagef("unknown command %q", args[0])
		}
		// Each command prints its own flags for --help.
		return c.run(ctx, a, []string{"--help"})
	default:
		return usagef("takes at most one command name")
	}
}

func runVersion(_ context.Context, a *app, args []string) error {
	fs := a.flags("version")
	if err := a.parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("takes no arguments")
	}
	fmt.Fprintf(a.stdout, "rexd %s\n", a.version)
	return nil
}

func moduleVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
