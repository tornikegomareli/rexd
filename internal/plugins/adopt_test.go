package plugins

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tornikegomareli/rexd/internal/state"
)

// luaPlugin makes a folder in the format Amber installs: no manifest, and a Lua module
// with a setup function.
func luaPlugin(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "thing")
	write(t, filepath.Join(dir, "lua/thing/init.lua"), "return { setup = function(opts) end }\n")
	return dir
}

func TestInstallAPluginWithNoManifest(t *testing.T) {
	e := newEnv(t)
	p, err := e.m.Install(context.Background(), Source{Kind: state.KindCopy, Location: luaPlugin(t)})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "local.thing" || p.Main != "thing" {
		t.Fatalf("%+v", p)
	}
	want := `require("thing").setup(rexd.plugin.options)`
	if got := read(t, e.layout.Module("local.thing")); !strings.Contains(got, want) {
		t.Fatalf("module:\n%s", got)
	}
	if got := read(t, e.layout.Index()); !strings.Contains(got, e.dirs.Plugin("local.thing")+"/lua/?.lua") {
		t.Fatalf("index:\n%s", got)
	}
}

func TestLinkAPluginWithNoManifestLeavesItsFolderAlone(t *testing.T) {
	e := newEnv(t)
	dir := luaPlugin(t)
	if _, err := e.m.Install(context.Background(), Source{Kind: state.KindLink, Location: dir}); err != nil {
		t.Fatal(err)
	}
	if fileExists(filepath.Join(dir, "rex-plugin.toml")) {
		t.Fatal("rexd wrote into the linked folder")
	}
}

func TestUpdateAPluginWithNoManifest(t *testing.T) {
	e := newEnv(t)
	dir := luaPlugin(t)
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "first")
	ctx := context.Background()
	p, err := e.m.Install(ctx, Source{Kind: state.KindGit, Location: "file://" + dir})
	if err != nil {
		t.Fatal(err)
	}
	first := p.Version
	write(t, filepath.Join(dir, "lua/thing/init.lua"), "return { setup = function(opts) end } -- new\n")
	runGit(t, dir, "commit", "-qam", "second")
	results, err := e.m.Update(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Changed || results[0].To == first {
		t.Fatalf("%+v", results)
	}
	if got := read(t, filepath.Join(e.dirs.Plugin(p.ID), "lua/thing/init.lua")); !strings.Contains(got, "new") {
		t.Fatalf("plugin: %s", got)
	}
}

func TestSetupBringsBackAPluginWithNoManifest(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.m.Install(ctx, Source{Kind: state.KindCopy, Location: luaPlugin(t)}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Teardown(ctx, TeardownOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Setup(ctx, SetupOptions{RexConfig: e.init}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, e.layout.Module("local.thing")), `require("thing")`) {
		t.Fatal("no module after setup")
	}
	if strings.Contains(e.out.String(), "skipping") {
		t.Fatalf("output:\n%s", e.out.String())
	}
}

func TestAFolderWithNoPluginIsRefused(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "README.md"), "hello")
	_, err := e.m.Install(context.Background(), Source{Kind: state.KindCopy, Location: dir})
	if err == nil || !strings.Contains(err.Error(), "not a rexd plugin") {
		t.Fatalf("err %v", err)
	}
}
