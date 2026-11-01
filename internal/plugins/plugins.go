// Package plugins installs, updates and removes plugins, and connects rexd to
// Rex's config.
//
// Install, update and setup are transactions. Steps that succeeded are undone when
// a later one fails, and Rex's own checker has the last word before anything is
// kept, so a bad plugin never leaves the user's Rex config broken.
package plugins

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/tornikegomareli/rexd/internal/daemon"
	"github.com/tornikegomareli/rexd/internal/fsutil"
	"github.com/tornikegomareli/rexd/internal/lockfile"
	"github.com/tornikegomareli/rexd/internal/manifest"
	"github.com/tornikegomareli/rexd/internal/paths"
	"github.com/tornikegomareli/rexd/internal/rex"
	"github.com/tornikegomareli/rexd/internal/rexconfig"
	"github.com/tornikegomareli/rexd/internal/state"
)

// ErrNotSetUp means rexd setup has not connected rexd to Rex's config yet.
var ErrNotSetUp = errors.New(`rexd is not connected to Rex yet; run "rexd setup" first`)

// Manager changes what is installed. It holds a lock while it does, so two
// commands never change the state at the same time.
type Manager struct {
	Dirs paths.Dirs
	// Rex runs rex commands. Its Server comes from the settings saved by Setup.
	Rex rex.Client
	// Out receives progress for the user.
	Out io.Writer

	syncing bool // Sync is running, and it leaves rexd.lock alone.
}

func (m *Manager) printf(format string, args ...any) {
	fmt.Fprintf(m.Out, format+"\n", args...)
}

// lock waits for any other rexd command that is changing things, such as one
// running a long build. It says so, because the wait would otherwise look like a hang.
func (m *Manager) lock(ctx context.Context) (*lockfile.Lock, error) {
	lock, err := lockfile.TryAcquire(m.Dirs.StateLock())
	if !errors.Is(err, lockfile.ErrHeld) {
		return lock, err
	}
	m.printf("Waiting for another rexd command to finish")
	return lockfile.Acquire(ctx, m.Dirs.StateLock())
}

// client returns the rex client for the saved settings.
func (m *Manager) client(current *state.State) rex.Client {
	client := m.Rex
	client.Server = current.Settings.Server
	return client
}

// loadSetUp reads the state and checks that rexd setup has run and still holds.
func (m *Manager) loadSetUp() (*state.State, rexconfig.Layout, error) {
	current, err := state.Load(m.Dirs.StateFile())
	if err != nil {
		return nil, rexconfig.Layout{}, err
	}
	if current.Settings.RexConfig == "" {
		return nil, rexconfig.Layout{}, ErrNotSetUp
	}
	layout := rexconfig.Layout{Init: current.Settings.RexConfig}
	has, err := layout.HasRequire()
	if err != nil {
		return nil, rexconfig.Layout{}, err
	}
	if !has {
		return nil, rexconfig.Layout{}, fmt.Errorf(`%s no longer loads rexd; run "rexd setup" to add the line back`, layout.Init)
	}
	return current, layout, nil
}

// undoStack holds the steps that reverse what a transaction did so far.
type undoStack []func() error

func (u *undoStack) push(step func() error) { *u = append(*u, step) }

func (u undoStack) run(out io.Writer) {
	for i := len(u) - 1; i >= 0; i-- {
		if err := u[i](); err != nil {
			fmt.Fprintf(out, "warning: could not undo a step: %v\n", err)
		}
	}
}

// entries lists the plugins that have config code, for the generated rexd.lua.
func (m *Manager) entries(current *state.State, layout rexconfig.Layout) []rexconfig.Entry {
	var entries []rexconfig.Entry
	for _, p := range current.SortedPlugins() {
		if fileExists(layout.Module(p.ID)) {
			entries = append(entries, m.entry(p))
		}
	}
	return entries
}

func (m *Manager) entry(p *state.Plugin) rexconfig.Entry {
	return rexconfig.Entry{ID: p.ID, Version: p.Version, Dir: m.Dirs.Plugin(p.ID), Options: p.Options}
}

// CheckConfigCode says if a plugin has config code in Rex, and what Rex's checker
// says about that code now.
func (m *Manager) CheckConfigCode(ctx context.Context, current *state.State, p *state.Plugin) (has bool, err error) {
	if current.Settings.RexConfig == "" {
		return false, nil
	}
	layout := rexconfig.Layout{Init: current.Settings.RexConfig}
	if !fileExists(layout.Module(p.ID)) {
		return false, nil
	}
	return true, layout.Check(ctx, m.client(current), m.entry(p))
}

// reload tells Rex to read its config. A failure here is not fatal, because the
// config passed Rex's checker and the change is already saved.
func (m *Manager) reload(ctx context.Context, client rex.Client) {
	switch err := client.ReloadConfig(ctx); {
	case err == nil:
	case errors.Is(err, rex.ErrNotRunning):
		m.printf("Rex is not running. It loads the new config when it starts.")
	default:
		m.printf("warning: Rex could not reload its config: %v", err)
	}
}

// notifyDaemon tells the daemon to start and stop scripts to match the state.
func (m *Manager) notifyDaemon(warnIfMissing bool) {
	notified, err := daemon.Notify(m.Dirs)
	switch {
	case err != nil:
		m.printf("warning: could not reach the rexd daemon: %v", err)
	case !notified && warnIfMissing:
		m.printf(`The rexd daemon is not running, so event scripts will not start. Run "rexd setup" to start it.`)
	}
}

func checkAPI(ctx context.Context, client rex.Client, man *manifest.Manifest) error {
	if man.RexAPI == 0 {
		return nil
	}
	version, err := client.Version(ctx)
	if err != nil {
		return err
	}
	if version.APIRevision < man.RexAPI {
		return fmt.Errorf("%s needs Rex API revision %d, and this Rex has %d; update Rex", man.ID, man.RexAPI, version.APIRevision)
	}
	return nil
}

// build runs a plugin's build steps in dir, which may be a temporary copy. The
// plugin's folder is where it ends up, for a build that embeds that path. No shell
// reads the commands.
func (m *Manager) build(ctx context.Context, man *manifest.Manifest, dir string) error {
	for _, step := range man.Build {
		m.printf("Building: %s", strings.Join(step.Run, " "))
		cmd := exec.CommandContext(ctx, step.Run[0], step.Run[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"REXD_PLUGIN_ID="+man.ID,
			"REXD_PLUGIN_VERSION="+man.Version,
			"REXD_PLUGIN_DIR="+m.Dirs.Plugin(man.ID),
		)
		cmd.Stdout, cmd.Stderr = m.Out, m.Out
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("build step %q failed: %w", strings.Join(step.Run, " "), err)
		}
	}
	return nil
}

func (m *Manager) stagingDir(pattern string) (string, func(), error) {
	if err := os.MkdirAll(m.Dirs.Staging(), 0o755); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp(m.Dirs.Staging(), pattern)
	if err != nil {
		return "", nil, err
	}
	return dir, func() { os.RemoveAll(dir) }, nil
}

// fetch makes a working copy of the source. A linked plugin has none, so it
// builds in place. Git commands never prompt, because a command waiting for a
// password would hang with no explanation.
func (m *Manager) fetch(ctx context.Context, src Source) (work, commit string, cleanup func(), err error) {
	cleanup = func() {}
	if src.Kind == state.KindLink {
		m.printf("Linking %s", src.Location)
		return src.Location, "", cleanup, nil
	}
	staging, cleanup, err := m.stagingDir("install-*")
	if err != nil {
		return "", "", nil, err
	}
	work = filepath.Join(staging, "plugin")
	switch src.Kind {
	case state.KindCopy:
		m.printf("Copying %s", src.Location)
		err = fsutil.CopyDir(src.Location, work)
	case state.KindGit:
		m.printf("Cloning %s", src.Location)
		commit, err = cloneRepo(ctx, src, work)
	default:
		err = fmt.Errorf("unknown source kind %q", src.Kind)
	}
	if err != nil {
		cleanup()
		return "", "", nil, err
	}
	return work, commit, cleanup, nil
}

func cloneRepo(ctx context.Context, src Source, dir string) (string, error) {
	ref := src.Ref
	if isRange(ref) {
		ref = src.Commit
		if ref == "" {
			tag, err := resolveRange(ctx, src.Location, src.Ref)
			if err != nil {
				return "", err
			}
			ref = tag
		}
	}
	if _, err := git(ctx, "", "clone", "--quiet", src.Location, dir); err != nil {
		return "", err
	}
	if ref != "" {
		if _, err := git(ctx, dir, "checkout", "--quiet", ref); err != nil {
			return "", err
		}
	}
	// On a branch, this keeps the branch, so the plugin can follow it later.
	if src.Commit != "" && ref != src.Commit {
		if _, err := git(ctx, dir, "reset", "--quiet", "--hard", src.Commit); err != nil {
			return "", err
		}
	}
	return git(ctx, dir, "rev-parse", "HEAD")
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], detail)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Install adds a plugin. It fetches and builds first, so a failure there touches
// nothing outside the staging folder.
func (m *Manager) Install(ctx context.Context, src Source) (plugin *state.Plugin, err error) {
	lock, err := m.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer lock.Release()

	current, layout, err := m.loadSetUp()
	if err != nil {
		return nil, err
	}
	client := m.client(current)
	if err := checkExistingConfig(ctx, client, layout, current); err != nil {
		return nil, err
	}
	plugin, err = m.install(ctx, client, current, layout, src, nil)
	if err != nil {
		return nil, err
	}
	if fileExists(layout.Module(plugin.ID)) {
		m.reload(ctx, client)
	}
	m.notifyDaemon(hasScripts(m.Dirs.Plugin(plugin.ID)))
	return plugin, nil
}

func hasScripts(dir string) bool {
	man, _, err := manifest.Load(dir)
	return err == nil && len(man.Scripts) > 0
}

// install fetches and builds a plugin, then makes it live.
func (m *Manager) install(ctx context.Context, client rex.Client, current *state.State, layout rexconfig.Layout, src Source, options map[string]any) (plugin *state.Plugin, err error) {
	work, commit, cleanup, err := m.fetch(ctx, src)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	// The working copy has a temporary name, so errors name the source.
	man, err := m.describe(work, src.Location, commit)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", src.Location, err)
	}
	if _, exists := current.Plugins[man.ID]; exists {
		return nil, fmt.Errorf(`%s is already installed; use "rexd update %s", or remove it first`, man.ID, man.ID)
	}
	if err := checkAPI(ctx, client, man); err != nil {
		return nil, err
	}
	if err := m.build(ctx, man, work); err != nil {
		return nil, err
	}
	sourceHash := ""
	if src.Kind != state.KindGit {
		if sourceHash, err = fsutil.HashDir(src.Location); err != nil {
			return nil, err
		}
	}

	var undo undoStack
	defer func() {
		if err != nil {
			undo.run(m.Out)
		}
	}()

	dir := m.Dirs.Plugin(man.ID)
	// Not in the state, so this can only be what an interrupted install left.
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(m.Dirs.Plugins(), 0o755); err != nil {
		return nil, err
	}
	link := src.Kind == state.KindLink
	if link {
		err = os.Symlink(src.Location, dir)
	} else {
		err = os.Rename(work, dir)
	}
	if err != nil {
		return nil, err
	}
	undo.push(func() error { return os.RemoveAll(dir) })

	now := time.Now()
	plugin = &state.Plugin{
		ID:          man.ID,
		Version:     man.Version,
		Kind:        src.Kind,
		Source:      src.Location,
		Ref:         src.Ref,
		Commit:      commit,
		Main:        man.Main,
		SourceHash:  sourceHash,
		Options:     options,
		Generation:  1,
		InstalledAt: now,
		UpdatedAt:   now,
	}
	err = m.activate(ctx, activation{
		current: current,
		layout:  layout,
		client:  client,
		man:     man,
		dir:     dir,
		link:    link,
		options: options,
		apply:   func() { current.Plugins[man.ID] = plugin },
		revert:  func() { delete(current.Plugins, man.ID) },
		configError: func(err error) error {
			return pluginConfigError(man.ID, err)
		},
	}, &undo)
	if err != nil {
		return nil, err
	}
	return plugin, nil
}

// describe reads a plugin's manifest. A plugin with none can still be a Lua module.
func (m *Manager) describe(dir, location, commit string) (*manifest.Manifest, error) {
	man, warnings, err := manifest.Load(dir)
	if errors.Is(err, manifest.ErrNotAPlugin) {
		return manifest.Adopt(dir, location, commit)
	}
	if err != nil {
		return nil, err
	}
	for _, warning := range warnings {
		m.printf("warning: %s", warning)
	}
	return man, nil
}

// activation is one version of a plugin to make live, in a folder that is already
// in place.
type activation struct {
	current *state.State
	layout  rexconfig.Layout
	client  rex.Client
	man     *manifest.Manifest
	dir     string
	link    bool
	options map[string]any
	// apply records the version in the state, and revert takes it out again.
	apply, revert func()
	// configError explains a failure from Rex's checker.
	configError func(error) error
}

// activate makes a version live: its config code, rexd.lua and the state, with Rex's
// checker deciding before anything is saved. On failure, the steps it pushed onto
// undo put everything back.
func (m *Manager) activate(ctx context.Context, a activation, undo *undoStack) error {
	restoreModule, err := m.replaceModule(a.layout, a.man, a.dir, a.link)
	if err != nil {
		return err
	}
	// One step, in this order: rexd.lua lists a plugin if its config file exists,
	// so the file has to be back before rexd.lua is written.
	undo.push(func() error {
		a.revert()
		if err := restoreModule(); err != nil {
			return err
		}
		return a.layout.WriteIndex(m.entries(a.current, a.layout))
	})
	if a.man.HasConfig() {
		m.printf("Checking the plugin's config")
		entry := rexconfig.Entry{ID: a.man.ID, Version: a.man.Version, Dir: a.dir, Options: a.options}
		if err := a.layout.Check(ctx, a.client, entry); err != nil {
			return a.configError(err)
		}
	}
	a.apply()
	if err := a.layout.WriteIndex(m.entries(a.current, a.layout)); err != nil {
		return err
	}
	if err := a.client.CheckConfig(ctx, a.layout.Init); err != nil {
		return a.configError(err)
	}
	return m.save(a.current, a.layout)
}

// brokenPluginError is a config check that failed inside an installed plugin.
type brokenPluginError struct {
	id     string
	detail string
}

func (e *brokenPluginError) Error() string {
	return fmt.Sprintf("the config code of %s has errors, so rexd changed nothing. Fix that plugin, or run \"rexd remove %s\":\n%s", e.id, e.id, e.detail)
}

// moduleInError finds a plugin's config file in what Rex's checker reports.
var moduleInError = regexp.MustCompile(`rexd/plugins/([^\s:]+)\.lua`)

// checkExistingConfig stops a change when the config is already broken. Otherwise
// the failure would look like the fault of the plugin being changed.
func checkExistingConfig(ctx context.Context, client rex.Client, layout rexconfig.Layout, current *state.State) error {
	err := client.CheckConfig(ctx, layout.Init)
	var configErr *rex.ConfigError
	if !errors.As(err, &configErr) {
		return err
	}
	if match := moduleInError.FindStringSubmatch(configErr.Error()); match != nil {
		id := strings.ReplaceAll(match[1], "/", ".")
		if _, installed := current.Plugins[id]; installed {
			return &brokenPluginError{id: id, detail: configErr.Error()}
		}
	}
	return fmt.Errorf("your Rex config has errors, so rexd changed nothing. Fix these first:\n%s", configErr.Error())
}

func pluginConfigError(id string, err error) error {
	var configErr *rex.ConfigError
	if errors.As(err, &configErr) {
		return fmt.Errorf("the config code of %s has errors, so rexd did not install it:\n%s", id, configErr.Error())
	}
	return err
}

// Remove deletes a plugin. A linked plugin only loses the link, and its source
// folder stays.
func (m *Manager) Remove(ctx context.Context, id string) error {
	lock, err := m.lock(ctx)
	if err != nil {
		return err
	}
	defer lock.Release()

	current, layout, err := m.loadSetUp()
	if err != nil {
		return err
	}
	if _, ok := current.Plugins[id]; !ok {
		return fmt.Errorf("%s is not installed", id)
	}
	hadModule := fileExists(layout.Module(id))

	delete(current.Plugins, id)
	if err := layout.RemoveModule(id); err != nil {
		return err
	}
	if err := layout.WriteIndex(m.entries(current, layout)); err != nil {
		return err
	}
	if err := m.save(current, layout); err != nil {
		return err
	}
	if hadModule {
		m.reload(ctx, m.client(current))
	}
	m.notifyDaemon(false)

	// os.RemoveAll removes a symbolic link and never what it points to.
	if err := os.RemoveAll(m.Dirs.Plugin(id)); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(m.Dirs.Logs(), id))
}

// UpdateResult says what Update did with one plugin.
type UpdateResult struct {
	ID string
	// From and To are the versions before and after.
	From, To string
	// Changed means the plugin was updated.
	Changed bool
	// Note explains a plugin that was left alone.
	Note string
}

// Update brings plugins up to date, all of them if ids is empty. A plugin that
// fails stays on its old version and the others continue.
func (m *Manager) Update(ctx context.Context, ids []string) ([]UpdateResult, error) {
	lock, err := m.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer lock.Release()

	current, layout, err := m.loadSetUp()
	if err != nil {
		return nil, err
	}
	client := m.client(current)

	targets := current.SortedPlugins()
	if len(ids) > 0 {
		targets = targets[:0]
		for _, id := range ids {
			p, ok := current.Plugins[id]
			if !ok {
				return nil, fmt.Errorf("%s is not installed", id)
			}
			targets = append(targets, p)
		}
	}

	// An update can fix the plugin that breaks the config, so that one goes first.
	// Until it is fixed, the checks of the others cannot pass.
	err = checkExistingConfig(ctx, client, layout, current)
	var broken *brokenPluginError
	if errors.As(err, &broken) {
		guilty := slices.IndexFunc(targets, func(p *state.Plugin) bool { return p.ID == broken.id })
		if guilty >= 0 {
			targets[0], targets[guilty] = targets[guilty], targets[0]
			err = nil
		}
	}
	if err != nil {
		return nil, err
	}

	var (
		results    []UpdateResult
		failures   []error
		anyChanged bool
		hasScripts bool
	)
	for _, p := range targets {
		result, man, err := m.updateOne(ctx, client, current, layout, p, "")
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", p.ID, err))
			continue
		}
		results = append(results, result)
		if result.Changed {
			anyChanged = true
			hasScripts = hasScripts || len(man.Scripts) > 0
		}
	}
	if anyChanged {
		// Even without config code now, the old version's may still be loaded.
		m.reload(ctx, client)
		m.notifyDaemon(hasScripts)
	}
	return results, errors.Join(failures...)
}

// updateOne replaces one plugin, keeping the old version until the new one has
// been built and has passed Rex's checker. With a target, it moves to that commit.
func (m *Manager) updateOne(ctx context.Context, client rex.Client, current *state.State, layout rexconfig.Layout, p *state.Plugin, target string) (result UpdateResult, man *manifest.Manifest, err error) {
	result = UpdateResult{ID: p.ID, From: p.Version, To: p.Version}
	dir := m.Dirs.Plugin(p.ID)

	work := dir // A linked plugin updates in place.
	commit := p.Commit
	link := p.Kind == state.KindLink
	if p.Kind != state.KindGit {
		hash, err := fsutil.HashDir(p.Source)
		if err != nil {
			return result, nil, fmt.Errorf("reading %s: %w", p.Source, err)
		}
		if hash == p.SourceHash {
			result.Note = "already up to date"
			return result, nil, nil
		}
	}
	if !link {
		staging, cleanup, err := m.stagingDir("update-*")
		if err != nil {
			return result, nil, err
		}
		defer cleanup()
		work = filepath.Join(staging, "plugin")
		switch p.Kind {
		case state.KindGit:
			if target == p.Commit {
				result.Note = "already at the locked commit"
				return result, nil, nil
			}
			var note string
			commit, note, err = m.fetchNewer(ctx, p, dir, work, target)
			if err != nil {
				return result, nil, err
			}
			if note != "" {
				result.Note = note
				return result, nil, nil
			}
		case state.KindCopy:
			if err := fsutil.CopyDir(p.Source, work); err != nil {
				return result, nil, fmt.Errorf("copying %s: %w", p.Source, err)
			}
		default:
			return result, nil, fmt.Errorf("unknown source kind %q", p.Kind)
		}
	}

	m.printf("Updating %s", p.ID)
	man, err = m.describe(work, p.Source, commit)
	if err != nil {
		return result, nil, err
	}
	if man.ID != p.ID {
		return result, nil, fmt.Errorf("its id changed to %s; remove it and install the new one", man.ID)
	}
	if err := checkAPI(ctx, client, man); err != nil {
		return result, nil, err
	}
	if err := m.build(ctx, man, work); err != nil {
		return result, nil, err
	}
	// After the build, because a linked folder holds what the build left.
	sourceHash := ""
	if p.Kind != state.KindGit {
		if sourceHash, err = fsutil.HashDir(p.Source); err != nil {
			return result, nil, err
		}
	}

	var undo undoStack
	defer func() {
		if err != nil {
			undo.run(m.Out)
		}
	}()

	if !link {
		old := filepath.Join(filepath.Dir(work), "old")
		if err := os.Rename(dir, old); err != nil {
			return result, nil, err
		}
		undo.push(func() error { return os.Rename(old, dir) })
		if err := os.Rename(work, dir); err != nil {
			return result, nil, err
		}
		undo.push(func() error { return os.RemoveAll(dir) })
	}

	previous := *p
	err = m.activate(ctx, activation{
		current: current,
		layout:  layout,
		client:  client,
		man:     man,
		dir:     dir,
		link:    link,
		options: p.Options,
		apply: func() {
			p.Version = man.Version
			p.Commit = commit
			p.Main = man.Main
			p.SourceHash = sourceHash
			p.Generation++
			p.UpdatedAt = time.Now()
		},
		revert:      func() { *p = previous },
		configError: updateConfigError,
	}, &undo)
	if err != nil {
		return result, nil, err
	}

	result.To, result.Changed = man.Version, true
	return result, man, nil
}

// fetchNewer clones what the plugin should move to into work, which is a fresh folder
// because the installed one holds what the build left. A note says why it stays.
func (m *Manager) fetchNewer(ctx context.Context, p *state.Plugin, dir, work, target string) (commit, note string, err error) {
	switch {
	case target != "":
		commit, err = cloneRepo(ctx, Source{Location: p.Source, Ref: p.Ref, Commit: target}, work)
		return commit, "", err
	case isRange(p.Ref):
		commit, err = cloneRepo(ctx, Source{Location: p.Source, Ref: p.Ref}, work)
	default:
		branch, err := git(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return "", "", err
		}
		if branch == "HEAD" { // Git's answer when no branch is checked out.
			return "", "pinned to " + p.Ref + "; remove and install it to change that", nil
		}
		commit, err = cloneRepo(ctx, Source{Location: p.Source, Ref: branch}, work)
		if err != nil {
			return "", "", err
		}
	}
	if err != nil {
		return "", "", err
	}
	if commit == p.Commit {
		return commit, "already up to date", nil
	}
	return commit, "", nil
}

func updateConfigError(err error) error {
	var configErr *rex.ConfigError
	if errors.As(err, &configErr) {
		return fmt.Errorf("the new config code has errors, so the old version stays:\n%s", configErr.Error())
	}
	return err
}

// replaceModule puts the new config code in place, and returns a step that puts
// back what was there.
func (m *Manager) replaceModule(layout rexconfig.Layout, man *manifest.Manifest, dir string, link bool) (restore func() error, err error) {
	target := layout.Module(man.ID)
	info, statErr := os.Lstat(target)
	switch {
	case statErr != nil:
		restore = func() error { return layout.RemoveModule(man.ID) }
	case info.Mode()&os.ModeSymlink != 0:
		source, err := os.Readlink(target)
		if err != nil {
			return nil, err
		}
		restore = func() error { return layout.InstallModule(man.ID, source, true) }
	default:
		data, err := os.ReadFile(target)
		if err != nil {
			return nil, err
		}
		restore = func() error { return fsutil.WriteFileAtomic(target, data, info.Mode().Perm()) }
	}
	if man.Main != "" {
		return restore, layout.WriteStarter(man.ID, man.Main)
	}
	if man.Config == "" {
		if err := layout.RemoveModule(man.ID); err != nil {
			return nil, err
		}
		return restore, nil
	}
	if err := layout.InstallModule(man.ID, filepath.Join(dir, filepath.Clean(man.Config)), link); err != nil {
		return nil, err
	}
	return restore, nil
}
