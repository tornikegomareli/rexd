package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/tornikegomareli/rexd/internal/manifest"
	"github.com/tornikegomareli/rexd/internal/rex"
	"github.com/tornikegomareli/rexd/internal/rexconfig"
	"github.com/tornikegomareli/rexd/internal/state"
)

// SetupOptions says how to connect rexd to Rex.
type SetupOptions struct {
	// RexConfig is the path of Rex's init.lua. Empty means to ask the running
	// server, then to use the saved setting, then Rex's default path.
	RexConfig string
	// Server is the value for rex -S. Empty keeps the saved setting.
	Server string
}

// Setup connects rexd to Rex's config: it adds the line to init.lua and writes
// the generated file. Running it again repairs whatever is missing.
func (m *Manager) Setup(ctx context.Context, opts SetupOptions) (settings *state.Settings, err error) {
	lock, err := m.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer lock.Release()

	current, err := state.Load(m.Dirs.StateFile())
	if err != nil {
		return nil, err
	}
	if opts.Server != "" {
		current.Settings.Server = opts.Server
	}
	client := m.client(current)

	path, err := resolveConfigPath(ctx, client, opts.RexConfig, current.Settings.RexConfig)
	if err != nil {
		return nil, err
	}
	layout := rexconfig.Layout{Init: path}

	var undo undoStack
	defer func() {
		if err != nil {
			undo.run(m.Out)
		}
	}()

	indexExisted := fileExists(layout.Index())
	if err := m.restoreModules(current, layout); err != nil {
		return nil, err
	}
	if err := layout.WriteIndex(m.entries(current, layout)); err != nil {
		return nil, err
	}
	if !indexExisted {
		undo.push(func() error { return layout.RemoveAll() })
	}
	restore, err := layout.AddRequire()
	if err != nil {
		return nil, err
	}
	undo.push(restore)

	if err := checkExistingConfig(ctx, client, layout); err != nil {
		return nil, err
	}

	current.Settings.RexConfig = path
	if err := current.Save(m.Dirs.StateFile()); err != nil {
		return nil, err
	}
	m.reload(ctx, client)
	return &current.Settings, nil
}

// resolveConfigPath finds Rex's init.lua. A running server knows for sure,
// because it may have been started with --config.
func resolveConfigPath(ctx context.Context, client rex.Client, flag, saved string) (string, error) {
	if flag != "" {
		return filepath.Abs(flag)
	}
	path, err := client.ConfigPath(ctx)
	if err == nil {
		return path, nil
	}
	if !errors.Is(err, rex.ErrNotRunning) {
		return "", err
	}
	if saved != "" {
		return saved, nil
	}
	return rex.LocalConfigPath()
}

// restoreModules puts config code back for plugins that are installed. Teardown
// removes it, and installed plugins come back on the next setup.
func (m *Manager) restoreModules(current *state.State, layout rexconfig.Layout) error {
	for _, p := range current.SortedPlugins() {
		dir := m.Dirs.Plugin(p.ID)
		man, _, err := manifest.Load(dir)
		if err != nil {
			m.printf("warning: skipping %s: %v", p.ID, err)
			continue
		}
		if man.Config == "" {
			continue
		}
		if err := layout.InstallModule(p.ID, filepath.Join(dir, filepath.Clean(man.Config)), p.Kind == state.KindLink); err != nil {
			return err
		}
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TeardownOptions says how far Teardown goes.
type TeardownOptions struct {
	// Purge also deletes installed plugins, the state and the logs.
	Purge bool
}

// Teardown undoes Setup. Installed plugins stay unless Purge is set, so a later
// setup brings them back. The caller stops the daemon first.
func (m *Manager) Teardown(ctx context.Context, opts TeardownOptions) error {
	lock, err := m.lock(ctx)
	if err != nil {
		return err
	}
	defer lock.Release()

	current, err := state.Load(m.Dirs.StateFile())
	if err != nil {
		return err
	}
	if path := current.Settings.RexConfig; path != "" {
		layout := rexconfig.Layout{Init: path}
		if err := layout.RemoveRequire(); err != nil {
			return err
		}
		if err := layout.RemoveAll(); err != nil {
			return err
		}
		m.reload(ctx, m.client(current))
	}
	if opts.Purge {
		if err := os.RemoveAll(m.Dirs.Data); err != nil {
			return err
		}
		// The lock file stays, because this process holds it.
		entries, err := os.ReadDir(m.Dirs.State)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			path := filepath.Join(m.Dirs.State, entry.Name())
			if path != m.Dirs.StateLock() {
				if err := os.RemoveAll(path); err != nil {
					return err
				}
			}
		}
		return nil
	}
	current.Settings = state.Settings{}
	return current.Save(m.Dirs.StateFile())
}
