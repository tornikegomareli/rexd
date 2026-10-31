package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadMissingFileIsEmpty(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Version != stateVersion || s.Plugins == nil || len(s.Plugins) != 0 {
		t.Fatalf("got %+v", s)
	}
}

func TestSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deep", "state.json")
	now := time.Date(2026, 10, 31, 4, 0, 0, 0, time.UTC)
	s := New()
	s.Settings = Settings{RexConfig: "/c/init.lua", Server: "unix:///s.sock"}
	s.Plugins["b.two"] = &Plugin{ID: "b.two", Version: "2", Kind: KindLink, Source: "/src", Generation: 3, InstalledAt: now, UpdatedAt: now}
	s.Plugins["a.one"] = &Plugin{ID: "a.one", Version: "1", Kind: KindGit, Source: "https://x", Ref: "main", Commit: "abc", Generation: 1, InstalledAt: now, UpdatedAt: now}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Settings != s.Settings {
		t.Errorf("settings = %+v", got.Settings)
	}
	sorted := got.SortedPlugins()
	if len(sorted) != 2 || sorted[0].ID != "a.one" || sorted[1].ID != "b.two" {
		t.Fatalf("sorted = %+v", sorted)
	}
	if p := sorted[0]; p.Kind != KindGit || p.Commit != "abc" || p.Ref != "main" || !p.InstalledAt.Equal(now) {
		t.Errorf("plugin = %+v", p)
	}
	if sorted[1].Generation != 3 {
		t.Errorf("generation = %d", sorted[1].Generation)
	}
}

func TestLoadRejectsNewerFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"version": 99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "newer rexd") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("no error for a corrupt file")
	}
}

func TestStatusRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	if s, err := LoadStatus(path); err != nil || s != nil {
		t.Fatalf("missing file: %v, %v", s, err)
	}
	now := time.Date(2026, 10, 31, 4, 0, 0, 0, time.UTC)
	want := &Status{
		Daemon: DaemonStatus{PID: 42, StartedAt: now, UpdatedAt: now, Rex: RexUp},
		Scripts: []ScriptStatus{{
			Plugin: "a.one", Script: "notify", State: ScriptBackoff, Since: now, Restarts: 2, NextStart: now.Add(time.Second),
			LastExit: &Exit{Code: 1, At: now, Ran: "3s"},
		}},
		PluginErrors: map[string]string{"b.two": "no manifest"},
	}
	if err := want.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != statusVersion || got.Daemon.PID != 42 || got.Daemon.Rex != RexUp {
		t.Errorf("daemon = %+v", got.Daemon)
	}
	s := got.Scripts[0]
	if s.State != ScriptBackoff || s.Restarts != 2 || !s.NextStart.Equal(now.Add(time.Second)) || s.LastExit.Code != 1 {
		t.Errorf("script = %+v", s)
	}
	if got.PluginErrors["b.two"] != "no manifest" {
		t.Errorf("plugin errors = %v", got.PluginErrors)
	}
}

func TestStatusOmitsEmptyFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	s := &Status{Scripts: []ScriptStatus{{Plugin: "a.one", Script: "s", State: ScriptRunning, PID: 7}}}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"next_start", "last_exit", "plugin_errors"} {
		if strings.Contains(string(data), field) {
			t.Errorf("%s should be omitted:\n%s", field, data)
		}
	}
}
