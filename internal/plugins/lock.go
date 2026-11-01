package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"reflect"
	"slices"
	"time"

	"github.com/tornikegomareli/rexd/internal/rexconfig"
	"github.com/tornikegomareli/rexd/internal/state"
)

// lockFile is rexd.lock: what another machine needs to get the same plugins. It has no
// times or paths, so it only changes when the plugins do. A plugin from a local folder
// stays out, because that folder is only on this machine.
type lockFile struct {
	Plugins map[string]lockedPlugin `json:"plugins"`
}

type lockedPlugin struct {
	Source  string         `json:"source"`
	Ref     string         `json:"ref,omitempty"`
	Commit  string         `json:"commit"`
	Options map[string]any `json:"options,omitempty"`
}

// save writes the state, and the lock that follows from it.
func (m *Manager) save(current *state.State, layout rexconfig.Layout) error {
	if err := current.Save(m.Dirs.StateFile()); err != nil {
		return err
	}
	if m.syncing {
		return nil // The lock is what a sync follows, so it decides at the end.
	}
	return writeLock(current, layout)
}

func writeLock(current *state.State, layout rexconfig.Layout) error {
	lock := lockFile{Plugins: map[string]lockedPlugin{}}
	for _, p := range current.Plugins {
		if p.Kind == state.KindGit {
			lock.Plugins[p.ID] = lockedPlugin{Source: p.Source, Ref: p.Ref, Commit: p.Commit, Options: p.Options}
		}
	}
	if len(lock.Plugins) == 0 {
		if err := os.Remove(layout.Lock()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}
	return layout.WriteLock(append(data, '\n'))
}

// SyncResult says what Sync did with one plugin.
type SyncResult struct {
	ID string
	// Changed means the plugin was installed or changed.
	Changed bool
	// Note says how it is now.
	Note string
}

// Sync makes the plugins match rexd.lock: it installs what is missing, moves the
// others to their locked commits, and sets their options. It removes nothing. A plugin
// that fails stays as it was, the others continue, and the lock stays as it is.
func (m *Manager) Sync(ctx context.Context) ([]SyncResult, error) {
	lock, err := m.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer lock.Release()

	current, layout, err := m.loadSetUp()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(layout.Lock())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s does not exist. rexd writes it when you install a plugin from git", layout.Lock())
	}
	if err != nil {
		return nil, err
	}
	var locked lockFile
	if err := json.Unmarshal(data, &locked); err != nil {
		return nil, fmt.Errorf("reading %s: %w", layout.Lock(), err)
	}
	client := m.client(current)
	if err := checkExistingConfig(ctx, client, layout, current); err != nil {
		return nil, err
	}

	m.syncing = true
	defer func() { m.syncing = false }()

	var (
		results    []SyncResult
		failures   []error
		anyChanged bool
	)
	for _, id := range slices.Sorted(maps.Keys(locked.Plugins)) {
		changed, note, err := m.syncOne(ctx, current, layout, id, locked.Plugins[id])
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", id, err))
			continue
		}
		results = append(results, SyncResult{ID: id, Changed: changed, Note: note})
		anyChanged = anyChanged || changed
	}
	for _, p := range current.SortedPlugins() {
		if _, ok := locked.Plugins[p.ID]; !ok {
			results = append(results, SyncResult{ID: p.ID, Note: "not in the lock, so it stays as it is"})
		}
	}
	if anyChanged {
		m.reload(ctx, client)
		m.notifyDaemon(false)
	}
	return results, errors.Join(failures...)
}

func (m *Manager) syncOne(ctx context.Context, current *state.State, layout rexconfig.Layout, id string, want lockedPlugin) (changed bool, note string, err error) {
	client := m.client(current)
	p, installed := current.Plugins[id]
	if !installed {
		src := Source{Kind: state.KindGit, Location: want.Source, Ref: want.Ref, Commit: want.Commit}
		plugin, err := m.install(ctx, client, current, layout, src, want.Options)
		if err != nil {
			return false, "", err
		}
		if plugin.ID != id {
			return true, "installed as " + plugin.ID, nil
		}
		return true, "installed " + plugin.Version, nil
	}
	if p.Kind != state.KindGit {
		return false, "installed from a local folder, so it stays as it is", nil
	}

	previous := *p
	p.Ref, p.Source = want.Ref, want.Source
	result, _, err := m.updateOne(ctx, client, current, layout, p, want.Commit)
	if err != nil {
		*p = previous
		return false, "", err
	}
	note = "at " + p.Version
	if reflect.DeepEqual(emptyToNil(p.Options), emptyToNil(want.Options)) {
		if p.Ref != previous.Ref && !result.Changed {
			return false, note, m.save(current, layout)
		}
		return result.Changed, note, nil
	}

	options := map[string]any{}
	for key := range p.Options {
		options[key] = nil
	}
	maps.Copy(options, want.Options)
	if err := m.applyOptions(ctx, current, layout, p, options); err != nil {
		return result.Changed, "", err
	}
	return true, note, nil
}

func emptyToNil(options map[string]any) map[string]any {
	if len(options) == 0 {
		return nil
	}
	return options
}

// touch records a change made now.
func touch(p *state.Plugin) {
	p.Generation++
	p.UpdatedAt = time.Now()
}
