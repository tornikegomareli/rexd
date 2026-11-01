package rexconfig

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tornikegomareli/rexd/internal/rex"
	"github.com/tornikegomareli/rexd/internal/testutil/fakerex"
)

func TestMain(m *testing.M) {
	os.Exit(fakerex.RunTests(m))
}

func layout(t *testing.T) Layout {
	t.Helper()
	return Layout{Init: filepath.Join(t.TempDir(), "rex", "init.lua")}
}

func writeInit(t *testing.T, l Layout, content string) {
	t.Helper()
	if err := os.MkdirAll(l.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.Init, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestLayoutPaths(t *testing.T) {
	l := Layout{Init: "/c/rex/init.lua"}
	if got := l.Index(); got != "/c/rex/rexd.lua" {
		t.Errorf("Index = %q", got)
	}
	if got := l.Module("acme.bell-notify"); got != "/c/rex/rexd/plugins/acme/bell-notify.lua" {
		t.Errorf("Module = %q", got)
	}
	if got := l.Module("a.b.c"); got != "/c/rex/rexd/plugins/a/b/c.lua" {
		t.Errorf("Module = %q", got)
	}
}

func TestIndexListsPluginsInOrder(t *testing.T) {
	got := string(Index([]Entry{
		{ID: "a.one", Version: "1.0", Dir: "/p/a.one"},
		{ID: "b.two", Version: "2.0", Dir: `/Application Support/b "two"`},
	}))
	if !strings.HasPrefix(got, generatedHeader) {
		t.Errorf("no header:\n%s", got)
	}
	first := strings.Index(got, `load_plugin("a.one", "1.0", "/p/a.one")`)
	second := strings.Index(got, `load_plugin("b.two", "2.0", "/Application Support/b \"two\"")`)
	if first < 0 || second < first {
		t.Errorf("plugin lines are wrong:\n%s", got)
	}
	if !strings.Contains(got, "pcall(require") {
		t.Errorf("plugins must load through pcall:\n%s", got)
	}
}

func TestLuaString(t *testing.T) {
	for in, want := range map[string]string{
		`plain`:       `"plain"`,
		`a"b`:         `"a\"b"`,
		`a\b`:         `"a\\b"`,
		"a\nb":        `"a\nb"`,
		"a\tb":        `"a\009b"`,
		"Tornike’s":   `"Tornike’s"`,
		"with space ": `"with space "`,
		"\x7f":        `"\127"`,
	} {
		if got := luaString(in); got != want {
			t.Errorf("luaString(%q) = %s, want %s", in, got, want)
		}
	}
}

// TestGeneratedLuaIsValid runs the generated Lua with a real interpreter, with
// stubs for what Rex provides, if the machine has one.
func TestGeneratedLuaIsValid(t *testing.T) {
	lua, err := exec.LookPath("lua")
	if err != nil {
		t.Skip("no lua interpreter on PATH")
	}
	dir := t.TempDir()
	l := Layout{Init: filepath.Join(dir, "init.lua")}
	entries := []Entry{
		{ID: "acme.good", Version: "1.0", Dir: `/dir with "quotes" and \backslash`},
		{ID: "acme.bad", Version: "2.0", Dir: "/bad"},
	}
	if err := l.WriteIndex(entries); err != nil {
		t.Fatal(err)
	}
	moduleDir := filepath.Join(dir, "rexd", "plugins", "acme")
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(moduleDir, "good.lua"), []byte(`seen_dir = rexd.plugin.dir; seen_id = rexd.plugin.id`), 0o644)
	os.WriteFile(filepath.Join(moduleDir, "bad.lua"), []byte(`this is not lua (`), 0o644)
	test := `
package.path = "./?.lua;" .. package.path
logged = {}
rex = { log = function(level, msg) table.insert(logged, level .. ": " .. msg) end }
require("rexd")
assert(seen_id == "acme.good", "plugin did not see its id")
assert(seen_dir == '/dir with "quotes" and \\backslash', "plugin did not see its dir: " .. tostring(seen_dir))
assert(rexd.plugin == nil, "rexd.plugin should be cleared")
assert(rexd.plugins["acme.bad"].version == "2.0")
assert(#logged == 1 and logged[1]:find("acme.bad", 1, true), "the broken plugin should be logged")
print("ok")
`
	cmd := exec.Command(lua, "-e", test)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ok") {
		t.Fatalf("lua: %v\n%s", err, out)
	}
}

func TestWriteIndexRefusesAFileItDidNotWrite(t *testing.T) {
	l := layout(t)
	writeInit(t, l, "")
	if err := os.WriteFile(l.Index(), []byte("-- my own module\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := l.WriteIndex(nil)
	if err == nil || !strings.Contains(err.Error(), "rexd did not write it") {
		t.Fatalf("err = %v", err)
	}
	if got := readFile(t, l.Index()); got != "-- my own module\n" {
		t.Fatalf("the user's file changed: %q", got)
	}
	if err := l.RemoveAll(); err == nil {
		t.Fatal("RemoveAll deleted a file rexd did not write")
	}
}

func TestWriteIndexReplacesItsOwnFile(t *testing.T) {
	l := layout(t)
	writeInit(t, l, "")
	if err := l.WriteIndex([]Entry{{ID: "a.one", Version: "1", Dir: "/x"}}); err != nil {
		t.Fatal(err)
	}
	if err := l.WriteIndex(nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFile(t, l.Index()), "a.one") {
		t.Fatal("old plugin is still listed")
	}
}

func TestInstallModuleCopies(t *testing.T) {
	l := layout(t)
	src := filepath.Join(t.TempDir(), "config.lua")
	os.WriteFile(src, []byte("-- one"), 0o644)
	if err := l.InstallModule("acme.foo", src, false); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(src, []byte("-- two"), 0o644)
	if got := readFile(t, l.Module("acme.foo")); got != "-- one" {
		t.Fatalf("a copy must not follow the source: %q", got)
	}
	if info, _ := os.Lstat(l.Module("acme.foo")); info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("expected a regular file")
	}
}

func TestInstallModuleLinks(t *testing.T) {
	l := layout(t)
	src := filepath.Join(t.TempDir(), "config.lua")
	os.WriteFile(src, []byte("-- one"), 0o644)
	if err := l.InstallModule("acme.foo", src, true); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(src, []byte("-- two"), 0o644)
	if got := readFile(t, l.Module("acme.foo")); got != "-- two" {
		t.Fatalf("a link must follow the source: %q", got)
	}
}

func TestInstallModuleKeepsTheOldFileIfTheCopyFails(t *testing.T) {
	l := layout(t)
	src := filepath.Join(t.TempDir(), "config.lua")
	os.WriteFile(src, []byte("-- one"), 0o644)
	if err := l.InstallModule("acme.foo", src, false); err != nil {
		t.Fatal(err)
	}
	if err := l.InstallModule("acme.foo", filepath.Join(t.TempDir(), "missing.lua"), false); err == nil {
		t.Fatal("no error for a missing source")
	}
	if got := readFile(t, l.Module("acme.foo")); got != "-- one" {
		t.Fatalf("module = %q", got)
	}
}

func TestInstallModuleReplacesAnExistingFile(t *testing.T) {
	l := layout(t)
	dir := t.TempDir()
	one, two := filepath.Join(dir, "one.lua"), filepath.Join(dir, "two.lua")
	os.WriteFile(one, []byte("-- one"), 0o644)
	os.WriteFile(two, []byte("-- two"), 0o644)
	if err := l.InstallModule("acme.foo", one, true); err != nil {
		t.Fatal(err)
	}
	if err := l.InstallModule("acme.foo", two, false); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, one); got != "-- one" {
		t.Fatalf("copying over a link wrote through it: %q", got)
	}
	if got := readFile(t, l.Module("acme.foo")); got != "-- two" {
		t.Fatalf("module = %q", got)
	}
}

func TestRemoveModuleCleansEmptyFolders(t *testing.T) {
	l := layout(t)
	src := filepath.Join(t.TempDir(), "config.lua")
	os.WriteFile(src, nil, 0o644)
	l.InstallModule("acme.foo", src, false)
	l.InstallModule("acme.bar", src, false)

	if err := l.RemoveModule("acme.foo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(l.Module("acme.bar")); err != nil {
		t.Fatalf("removed the wrong plugin: %v", err)
	}
	if err := l.RemoveModule("acme.bar"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(l.ModuleRoot(), "plugins", "acme")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty folder left behind: %v", err)
	}
	if _, err := os.Stat(filepath.Join(l.ModuleRoot(), "plugins")); err != nil {
		t.Fatalf("removed the plugins folder: %v", err)
	}
	if err := l.RemoveModule("acme.gone"); err != nil {
		t.Fatalf("removing a missing module: %v", err)
	}
}

func TestRemoveAll(t *testing.T) {
	l := layout(t)
	writeInit(t, l, "-- mine\n")
	src := filepath.Join(t.TempDir(), "config.lua")
	os.WriteFile(src, nil, 0o644)
	l.InstallModule("acme.foo", src, false)
	l.WriteIndex(nil)

	if err := l.RemoveAll(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{l.Index(), l.ModuleRoot()} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists", path)
		}
	}
	if got := readFile(t, l.Init); got != "-- mine\n" {
		t.Fatalf("init.lua changed: %q", got)
	}
}

func TestRemoveAllKeepsTheUsersFilesInTheFolder(t *testing.T) {
	l := layout(t)
	writeInit(t, l, "")
	mine := filepath.Join(l.ModuleRoot(), "mine.lua")
	if err := os.MkdirAll(l.ModuleRoot(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mine, []byte("-- mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "config.lua")
	os.WriteFile(src, nil, 0o644)
	if err := l.InstallModule("acme.foo", src, false); err != nil {
		t.Fatal(err)
	}
	if err := l.WriteIndex(nil); err != nil {
		t.Fatal(err)
	}

	if err := l.RemoveAll(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, mine); got != "-- mine" {
		t.Errorf("the user's file = %q", got)
	}
	if _, err := os.Stat(l.Module("acme.foo")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("rexd's file is still there: %v", err)
	}
}

func TestCheck(t *testing.T) {
	f := fakerex.New(t)
	client := rex.Client{Path: f.Path}
	l := layout(t)
	writeInit(t, l, "")
	src := filepath.Join(t.TempDir(), "config.lua")
	entry := Entry{ID: "acme.foo", Version: "1", Dir: "/p"}

	os.WriteFile(src, []byte("-- fine"), 0o644)
	l.InstallModule("acme.foo", src, false)
	if err := l.Check(context.Background(), client, entry); err != nil {
		t.Fatal(err)
	}

	os.WriteFile(src, []byte("BROKEN"), 0o644)
	l.InstallModule("acme.foo", src, false)
	err := l.Check(context.Background(), client, entry)
	var configErr *rex.ConfigError
	if !errors.As(err, &configErr) {
		t.Fatalf("got %v, want a ConfigError", err)
	}

	entries, _ := os.ReadDir(l.Dir())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".rexd-check") {
			t.Fatalf("left %s behind", e.Name())
		}
	}
}

func TestHasRequire(t *testing.T) {
	for content, want := range map[string]bool{
		`require("rexd")`:                       true,
		`require "rexd"`:                        true,
		`require 'rexd'`:                        true,
		`  require("rexd") -- comment`:          true,
		"-- first\nrequire(\"rexd\")\nmore()\n": true,
		`-- require("rexd")`:                    false,
		`require("rexd.plugins.a")`:             false,
		`require("rexdother")`:                  false,
		`local x = require("rexd")`:             false,
		``:                                      false,
	} {
		l := layout(t)
		writeInit(t, l, content)
		got, err := l.HasRequire()
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("HasRequire(%q) = %v, want %v", content, got, want)
		}
	}
}

func TestHasRequireIgnoresBlockComments(t *testing.T) {
	for content, want := range map[string]bool{
		"--[[\nrequire(\"rexd\")\n]]\n":                  false,
		"--[==[\nrequire(\"rexd\")\n]==]\n":              false,
		"--[==[\n]]\nrequire(\"rexd\")\n]==]\n":          false,
		"--[[ unterminated\nrequire(\"rexd\")\n":         false,
		"--[[ note ]]\nrequire(\"rexd\")\n":              true,
		"--[[\nold\n]]\nrex.bind()\nrequire(\"rexd\")\n": true,
		"-- [[ only a line comment\nrequire(\"rexd\")\n": true,
	} {
		l := layout(t)
		writeInit(t, l, content)
		got, err := l.HasRequire()
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("HasRequire(%q) = %v, want %v", content, got, want)
		}
	}
}

func TestHasRequireWithoutAFile(t *testing.T) {
	got, err := layout(t).HasRequire()
	if err != nil || got {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestAddRequireCreatesTheFile(t *testing.T) {
	l := layout(t)
	if _, err := l.AddRequire(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, l.Init); got != requireLine {
		t.Fatalf("init.lua = %q", got)
	}
}

func TestAddRequireGoesFirstAndKeepsTheRest(t *testing.T) {
	l := layout(t)
	writeInit(t, l, "rex.bind(\"cmd+d\", \"pane.zoom\")\n")
	if _, err := l.AddRequire(); err != nil {
		t.Fatal(err)
	}
	want := requireLine + "rex.bind(\"cmd+d\", \"pane.zoom\")\n"
	if got := readFile(t, l.Init); got != want {
		t.Fatalf("init.lua = %q", got)
	}
}

func TestAddRequireIsIdempotent(t *testing.T) {
	l := layout(t)
	writeInit(t, l, "rex.bind(\"cmd+d\", \"pane.zoom\")\nrequire 'rexd'\n")
	before := readFile(t, l.Init)
	if _, err := l.AddRequire(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, l.Init); got != before {
		t.Fatalf("init.lua changed: %q", got)
	}
}

func TestAddRequireKeepsTheFileMode(t *testing.T) {
	l := layout(t)
	writeInit(t, l, "-- mine\n")
	if err := os.Chmod(l.Init, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := l.AddRequire(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(l.Init)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
}

func TestAddRequireRestore(t *testing.T) {
	l := layout(t)
	writeInit(t, l, "-- mine\n")
	restore, err := l.AddRequire()
	if err != nil {
		t.Fatal(err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, l.Init); got != "-- mine\n" {
		t.Fatalf("init.lua = %q", got)
	}

	created := layout(t)
	restore, err = created.AddRequire()
	if err != nil {
		t.Fatal(err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(created.Init); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore should delete a file it created: %v", err)
	}
}

// People keep init.lua in a dotfiles repository, with a link in the config folder.
func TestRequireEditsGoThroughASymlink(t *testing.T) {
	l := layout(t)
	target := filepath.Join(t.TempDir(), "dotfiles-init.lua")
	if err := os.WriteFile(target, []byte("-- mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(l.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, l.Init); err != nil {
		t.Fatal(err)
	}
	assertLink := func(when string) {
		t.Helper()
		info, err := os.Lstat(l.Init)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s: init.lua is no longer a link", when)
		}
	}

	restore, err := l.AddRequire()
	if err != nil {
		t.Fatal(err)
	}
	assertLink("after AddRequire")
	if got := readFile(t, target); got != requireLine+"-- mine\n" {
		t.Errorf("target = %q", got)
	}
	if info, _ := os.Stat(target); info.Mode().Perm() != 0o600 {
		t.Errorf("target mode = %v", info.Mode().Perm())
	}

	if err := restore(); err != nil {
		t.Fatal(err)
	}
	assertLink("after restore")
	if got := readFile(t, target); got != "-- mine\n" {
		t.Errorf("restored target = %q", got)
	}

	if _, err := l.AddRequire(); err != nil {
		t.Fatal(err)
	}
	if err := l.RemoveRequire(); err != nil {
		t.Fatal(err)
	}
	assertLink("after RemoveRequire")
	if got := readFile(t, target); got != "-- mine\n" {
		t.Errorf("target after RemoveRequire = %q", got)
	}
}

func TestRemoveRequireKeepsALinkThatWouldBeLeftEmpty(t *testing.T) {
	l := layout(t)
	target := filepath.Join(t.TempDir(), "dotfiles-init.lua")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(l.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, l.Init); err != nil {
		t.Fatal(err)
	}
	if _, err := l.AddRequire(); err != nil {
		t.Fatal(err)
	}
	if err := l.RemoveRequire(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(l.Init); err != nil {
		t.Fatalf("the link is gone: %v", err)
	}
	if got := readFile(t, target); got != "" {
		t.Errorf("target = %q", got)
	}
}

func TestRemoveRequire(t *testing.T) {
	l := layout(t)
	writeInit(t, l, "-- before\n"+requireLine+"rex.bind(\"cmd+d\", \"pane.zoom\")\n")
	if err := l.RemoveRequire(); err != nil {
		t.Fatal(err)
	}
	want := "-- before\nrex.bind(\"cmd+d\", \"pane.zoom\")\n"
	if got := readFile(t, l.Init); got != want {
		t.Fatalf("init.lua = %q", got)
	}
}

func TestRemoveRequireKeepsOtherCode(t *testing.T) {
	for content, want := range map[string]string{
		// The comment on rexd's own line goes with it.
		"require(\"rexd\") -- note\nmine()\n": "mine()\n",
		"require 'rexd'\nmine()\n":            "mine()\n",
		"require(\"rexd\");\nmine()\n":        "mine()\n",
		// Other code on the line stays, and so does its indentation.
		"require(\"rexd\"); require(\"mine\")\n": "require(\"mine\")\n",
		"  require(\"rexd\") mine()\nx()\n":      "  mine()\nx()\n",
		// This uses the module's value, so removing the call would break the line.
		"require(\"rexd\").x = 1\nmine()\n": "require(\"rexd\").x = 1\nmine()\n",
		// A comment is not code.
		"--[[\nrequire(\"rexd\")\n]]\nmine()\n":                    "--[[\nrequire(\"rexd\")\n]]\nmine()\n",
		"--[[\nrequire(\"rexd\")\n]]\nrequire(\"rexd\")\nmine()\n": "--[[\nrequire(\"rexd\")\n]]\nmine()\n",
	} {
		l := layout(t)
		writeInit(t, l, content)
		if err := l.RemoveRequire(); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, l.Init); got != want {
			t.Errorf("RemoveRequire(%q) = %q, want %q", content, got, want)
		}
	}
}

func TestAddRequireWhenOnlyACommentHasIt(t *testing.T) {
	l := layout(t)
	content := "--[[\nrequire(\"rexd\")\n]]\n"
	writeInit(t, l, content)
	if _, err := l.AddRequire(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, l.Init); got != requireLine+content {
		t.Errorf("init.lua = %q", got)
	}
}

func TestRemoveRequireDeletesAFileItWouldLeaveEmpty(t *testing.T) {
	l := layout(t)
	if _, err := l.AddRequire(); err != nil {
		t.Fatal(err)
	}
	if err := l.RemoveRequire(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(l.Init); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file should be gone: %v", err)
	}
}

func TestRemoveRequireWithoutTheLine(t *testing.T) {
	l := layout(t)
	if err := l.RemoveRequire(); err != nil {
		t.Fatalf("no file: %v", err)
	}
	writeInit(t, l, "-- mine\n")
	if err := l.RemoveRequire(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, l.Init); got != "-- mine\n" {
		t.Fatalf("init.lua = %q", got)
	}
}

func TestIndexMakesAPluginsLuaFilesFindable(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "lua"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := string(Index([]Entry{{ID: "a.b", Version: "1", Dir: dir}}))
	want := `package.path = "` + dir + `/lua/?.lua;` + dir + `/lua/?/init.lua;" .. package.path`
	if !strings.Contains(got, want) {
		t.Fatalf("no %q in:\n%s", want, got)
	}
}

func TestRexLuaWinsOverLua(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"lua", "rex/lua"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := string(Index([]Entry{{ID: "a.b", Version: "1", Dir: dir}}))
	if !strings.Contains(got, dir+"/rex/lua/?.lua") || strings.Contains(got, dir+"/lua/?.lua") {
		t.Fatalf("got:\n%s", got)
	}
}
