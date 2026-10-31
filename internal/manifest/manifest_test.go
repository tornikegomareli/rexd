package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plugin writes a plugin folder with a manifest and the given empty Lua files.
func plugin(t *testing.T, manifest string, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		path := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const valid = `
id = "acme.bell-notify"
name = "Bell notify"
version = "0.1.0"
description = "Turns a terminal bell into a notification."
homepage = "https://example.com"
license = "MIT"
rex_api = 7
config = "rex/config.lua"

[[scripts]]
name = "notify"
path = "rex/notify.lua"

[[scripts]]
name = "second-one"
path = "rex/second.lua"

[[build]]
run = ["make", "build"]
`

func TestLoadValid(t *testing.T) {
	dir := plugin(t, valid, "rex/config.lua", "rex/notify.lua", "rex/second.lua")
	m, warnings, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v", warnings)
	}
	if m.ID != "acme.bell-notify" || m.Name != "Bell notify" || m.Version != "0.1.0" || m.RexAPI != 7 {
		t.Errorf("fields: %+v", m)
	}
	if m.Config != "rex/config.lua" {
		t.Errorf("config = %q", m.Config)
	}
	if len(m.Scripts) != 2 || m.Scripts[1].Name != "second-one" || m.Scripts[1].Path != "rex/second.lua" {
		t.Errorf("scripts = %+v", m.Scripts)
	}
	if len(m.Build) != 1 || strings.Join(m.Build[0].Run, " ") != "make build" {
		t.Errorf("build = %+v", m.Build)
	}
}

func TestLoadAcceptsConfigOnlyAndScriptOnly(t *testing.T) {
	configOnly := plugin(t, "id=\"a.b\"\nname=\"n\"\nversion=\"1\"\nconfig=\"c.lua\"\n", "c.lua")
	if _, _, err := Load(configOnly); err != nil {
		t.Errorf("config only: %v", err)
	}
	scriptOnly := plugin(t, "id=\"a.b\"\nname=\"n\"\nversion=\"1\"\n[[scripts]]\nname=\"s\"\npath=\"s.lua\"\n", "s.lua")
	if _, _, err := Load(scriptOnly); err != nil {
		t.Errorf("script only: %v", err)
	}
}

func TestLoadWarnsAboutUnknownKeys(t *testing.T) {
	dir := plugin(t, "id=\"a.b\"\nname=\"n\"\nversion=\"1\"\nconfig=\"c.lua\"\nfuture_key=true\n", "c.lua")
	_, warnings, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "future_key") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestLoadMissingManifest(t *testing.T) {
	_, _, err := Load(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "not a rexd plugin") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadInvalidTOML(t *testing.T) {
	_, _, err := Load(plugin(t, "id = "))
	if err == nil || !strings.Contains(err.Error(), "reading rex-plugin.toml") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidationProblems(t *testing.T) {
	base := "name=\"n\"\nversion=\"1\"\nconfig=\"c.lua\"\n"
	tests := []struct {
		name     string
		manifest string
		want     string
	}{
		{"missing id", base, "id is required"},
		{"id without a dot", `id="plugin"` + "\n" + base, "must look like"},
		{"id with uppercase", `id="Acme.plugin"` + "\n" + base, "must look like"},
		{"id with a path", `id="../evil.plugin"` + "\n" + base, "must look like"},
		{"id with a slash", `id="acme/plugin"` + "\n" + base, "must look like"},
		{"id too long", `id="a.` + strings.Repeat("b", 70) + `"` + "\n" + base, "longer than"},
		{"missing name", "id=\"a.b\"\nversion=\"1\"\nconfig=\"c.lua\"\n", "name is required"},
		{"missing version", "id=\"a.b\"\nname=\"n\"\nconfig=\"c.lua\"\n", "version is required"},
		{"version with a space", "id=\"a.b\"\nname=\"n\"\nversion=\"1 2\"\nconfig=\"c.lua\"\n", "must not contain spaces"},
		{"negative api", "id=\"a.b\"\nname=\"n\"\nversion=\"1\"\nconfig=\"c.lua\"\nrex_api=-1\n", "rex_api"},
		{"nothing to run", "id=\"a.b\"\nname=\"n\"\nversion=\"1\"\n", "needs a config file"},
		{"config missing", "id=\"a.b\"\nname=\"n\"\nversion=\"1\"\nconfig=\"gone.lua\"\n", "does not exist"},
		{"config outside", "id=\"a.b\"\nname=\"n\"\nversion=\"1\"\nconfig=\"../c.lua\"\n", "outside the plugin"},
		{"config absolute", "id=\"a.b\"\nname=\"n\"\nversion=\"1\"\nconfig=\"/etc/c.lua\"\n", "must be relative"},
		{"config not lua", "id=\"a.b\"\nname=\"n\"\nversion=\"1\"\nconfig=\"c.txt\"\n", "must be a .lua file"},
		{"script without a name", base2(`path="c.lua"`), "name is required"},
		{"script with a bad name", base2(`name="Bad Name"`, `path="c.lua"`), "must use lowercase"},
		{"script without a path", base2(`name="s"`), "path is required"},
		{"build without a program", "id=\"a.b\"\nname=\"n\"\nversion=\"1\"\nconfig=\"c.lua\"\n[[build]]\nrun=[]\n", "run needs a program"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Load(plugin(t, tt.manifest, "c.lua", "c.txt"))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func base2(script ...string) string {
	return "id=\"a.b\"\nname=\"n\"\nversion=\"1\"\n[[scripts]]\n" + strings.Join(script, "\n") + "\n"
}

func TestDuplicateScriptNames(t *testing.T) {
	m := "id=\"a.b\"\nname=\"n\"\nversion=\"1\"\n[[scripts]]\nname=\"s\"\npath=\"c.lua\"\n[[scripts]]\nname=\"s\"\npath=\"c.lua\"\n"
	_, _, err := Load(plugin(t, m, "c.lua"))
	if err == nil || !strings.Contains(err.Error(), "used twice") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidationReportsEveryProblem(t *testing.T) {
	_, _, err := Load(plugin(t, `config="gone.lua"`))
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"id is required", "name is required", "version is required", "does not exist"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q missing from: %v", want, err)
		}
	}
}
