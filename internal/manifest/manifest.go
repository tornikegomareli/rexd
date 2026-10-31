// Package manifest reads and validates rex-plugin.toml, the file that describes a plugin.
package manifest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// FileName is the name of the manifest at the top of a plugin.
const FileName = "rex-plugin.toml"

const maxIDLength = 64

var (
	// An id is at least two dot-separated segments, such as "acme.bell-notify".
	// Segments become folders and Lua module names, so the alphabet stays small.
	idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(\.[a-z0-9][a-z0-9-]*)+$`)
	// A script name becomes a file name and appears in status output.
	scriptNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

// Manifest describes a plugin.
type Manifest struct {
	// ID identifies the plugin, such as "acme.bell-notify".
	ID string `toml:"id"`
	// Name is the plugin's name for people.
	Name string `toml:"name"`
	// Version is the plugin's version, such as "0.1.0".
	Version string `toml:"version"`
	// Description says what the plugin does in one sentence.
	Description string `toml:"description"`
	// Homepage is an optional URL.
	Homepage string `toml:"homepage"`
	// License is an optional SPDX identifier, such as "MIT".
	License string `toml:"license"`
	// RexAPI is the lowest Rex API revision the plugin works with. Zero means any.
	RexAPI int `toml:"rex_api"`
	// Config is the path of the Lua file loaded into Rex's config. It defines
	// actions and key bindings.
	Config string `toml:"config"`
	// Scripts are Lua files the daemon keeps running to receive events.
	Scripts []Script `toml:"scripts"`
	// Build lists commands to run in the plugin's folder on install and update.
	Build []Build `toml:"build"`
}

// Script is an event script that the daemon keeps running.
type Script struct {
	// Name identifies the script within the plugin.
	Name string `toml:"name"`
	// Path is the Lua file, relative to the plugin's folder.
	Path string `toml:"path"`
}

// Build is one build step.
type Build struct {
	// Run is the program and its arguments. No shell reads it.
	Run []string `toml:"run"`
}

// Load reads and validates the manifest in dir. Keys rexd does not know come
// back as warnings, so a plugin written for a newer rexd still loads.
func Load(dir string) (*Manifest, []string, error) {
	path := filepath.Join(dir, FileName)
	var m Manifest
	meta, err := toml.DecodeFile(path, &m)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("%s has no %s, so it is not a rexd plugin", dir, FileName)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", FileName, err)
	}
	if err := m.validate(dir); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", FileName, err)
	}
	var warnings []string
	for _, key := range meta.Undecoded() {
		warnings = append(warnings, fmt.Sprintf("%s: unknown key %q is ignored", FileName, key.String()))
	}
	return &m, warnings, nil
}

// validate reports every problem at once, so an author fixes them in one pass.
func (m *Manifest) validate(dir string) error {
	var problems []error
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Errorf(format, args...))
	}

	switch {
	case m.ID == "":
		add("id is required")
	case len(m.ID) > maxIDLength:
		add("id %q is longer than %d characters", m.ID, maxIDLength)
	case !idPattern.MatchString(m.ID):
		add(`id %q must look like "owner.name": lowercase letters, digits and hyphens, separated by dots`, m.ID)
	}
	if strings.TrimSpace(m.Name) == "" {
		add("name is required")
	}
	if m.Version == "" {
		add("version is required")
	} else if strings.ContainsAny(m.Version, " \t\r\n") {
		add("version %q must not contain spaces", m.Version)
	}
	if m.RexAPI < 0 {
		add("rex_api must not be negative")
	}
	if m.Config == "" && len(m.Scripts) == 0 {
		add("a plugin needs a config file, at least one script, or both")
	}
	if m.Config != "" {
		if err := checkLuaFile(dir, m.Config); err != nil {
			add("config: %w", err)
		}
	}
	seen := map[string]bool{}
	for i, s := range m.Scripts {
		label := fmt.Sprintf("scripts[%d]", i)
		switch {
		case s.Name == "":
			add("%s: name is required", label)
		case !scriptNamePattern.MatchString(s.Name):
			add("%s: name %q must use lowercase letters, digits and hyphens", label, s.Name)
		case seen[s.Name]:
			add("%s: name %q is used twice", label, s.Name)
		}
		seen[s.Name] = true
		if s.Path == "" {
			add("%s: path is required", label)
		} else if err := checkLuaFile(dir, s.Path); err != nil {
			add("%s: %w", label, err)
		}
	}
	for i, b := range m.Build {
		if len(b.Run) == 0 || b.Run[0] == "" {
			add("build[%d]: run needs a program", i)
		}
	}
	return errors.Join(problems...)
}

// checkLuaFile checks that rel names a Lua file inside dir.
func checkLuaFile(dir, rel string) error {
	if filepath.IsAbs(rel) {
		return fmt.Errorf("path %q must be relative to the plugin", rel)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q is outside the plugin", rel)
	}
	if filepath.Ext(clean) != ".lua" {
		return fmt.Errorf("path %q must be a .lua file", rel)
	}
	info, err := os.Stat(filepath.Join(dir, clean))
	if err != nil {
		return fmt.Errorf("path %q does not exist", rel)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("path %q is not a file", rel)
	}
	return nil
}
