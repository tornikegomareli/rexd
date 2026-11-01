// Package state reads and writes the files rexd keeps between runs: the list of
// installed plugins, which the command line owns, and the status of event
// scripts, which the daemon owns.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/tornikegomareli/rexd/internal/fsutil"
)

const stateVersion = 1

// Kind says how a plugin got onto this machine.
type Kind string

const (
	// KindGit is a clone of a git repository.
	KindGit Kind = "git"
	// KindCopy is a copy of a local folder.
	KindCopy Kind = "copy"
	// KindLink is a symbolic link to a local folder, for plugin development.
	KindLink Kind = "link"
)

// State is the content of state.json.
type State struct {
	Version  int                `json:"version"`
	Settings Settings           `json:"settings"`
	Plugins  map[string]*Plugin `json:"plugins"`
}

// Settings are chosen by rexd setup and used by every later command.
type Settings struct {
	// RexConfig is the path of Rex's init.lua. Empty until rexd setup runs.
	RexConfig string `json:"rex_config,omitempty"`
	// Server is the value passed to rex -S. Empty means Rex's default server.
	Server string `json:"server,omitempty"`
}

// Plugin records one installed plugin.
type Plugin struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Kind    Kind   `json:"kind"`
	// Source is the clone URL or the absolute path of the local folder.
	Source string `json:"source"`
	// Ref is the branch, tag or commit asked for at install. Empty means the default branch.
	Ref string `json:"ref,omitempty"`
	// Commit is the checked out commit of a git plugin.
	Commit string `json:"commit,omitempty"`
	// Options are the values set with rexd set: strings, numbers and booleans.
	Options map[string]any `json:"options,omitempty"`
	// Generation goes up on every install and update. The daemon restarts a
	// plugin's scripts when it changes.
	Generation  int       `json:"generation"`
	InstalledAt time.Time `json:"installed_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// New returns an empty state.
func New() *State {
	return &State{Version: stateVersion, Plugins: map[string]*Plugin{}}
}

// Load reads the state at path. A missing file is an empty state.
func Load(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return nil, err
	}
	s := New()
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if s.Version > stateVersion {
		return nil, fmt.Errorf("%s was written by a newer rexd (format %d); update rexd", path, s.Version)
	}
	if s.Plugins == nil {
		s.Plugins = map[string]*Plugin{}
	}
	return s, nil
}

// Save writes the state to path atomically.
func (s *State) Save(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, append(data, '\n'), 0o644)
}

// SortedPlugins returns the plugins ordered by id.
func (s *State) SortedPlugins() []*Plugin {
	plugins := make([]*Plugin, 0, len(s.Plugins))
	for _, p := range s.Plugins {
		plugins = append(plugins, p)
	}
	slices.SortFunc(plugins, func(a, b *Plugin) int { return strings.Compare(a.ID, b.ID) })
	return plugins
}
