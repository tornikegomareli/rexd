package plugins

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tornikegomareli/rexd/internal/lockfile"
	"github.com/tornikegomareli/rexd/internal/paths"
	"github.com/tornikegomareli/rexd/internal/rex"
	"github.com/tornikegomareli/rexd/internal/rexconfig"
	"github.com/tornikegomareli/rexd/internal/state"
	"github.com/tornikegomareli/rexd/internal/testutil/fakerex"
)

func TestMain(m *testing.M) {
	os.Exit(fakerex.RunTests(m))
}

type env struct {
	t      *testing.T
	fake   *fakerex.Fake
	dirs   paths.Dirs
	init   string
	out    *bytes.Buffer
	m      *Manager
	layout rexconfig.Layout
}

// newEnv returns a manager that has been set up against a fake Rex.
func newEnv(t *testing.T) *env {
	t.Helper()
	e := newBareEnv(t)
	if _, err := e.m.Setup(context.Background(), SetupOptions{RexConfig: e.init}); err != nil {
		t.Fatal(err)
	}
	return e
}

// newBareEnv returns a manager before setup.
func newBareEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	fake := fakerex.New(t)
	dirs := paths.Dirs{Data: filepath.Join(root, "data"), State: filepath.Join(root, "state")}
	init := filepath.Join(root, "config", "rex", "init.lua")
	out := &bytes.Buffer{}
	return &env{
		t:      t,
		fake:   fake,
		dirs:   dirs,
		init:   init,
		out:    out,
		m:      &Manager{Dirs: dirs, Rex: rex.Client{Path: fake.Path, Env: fake.Env}, Out: out},
		layout: rexconfig.Layout{Init: init},
	}
}

func (e *env) state() *state.State {
	e.t.Helper()
	s, err := state.Load(e.dirs.StateFile())
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

// pluginFiles writes a plugin folder and returns its path.
func pluginFiles(t *testing.T, id, version, extraManifest string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	manifest := fmt.Sprintf("id = %q\nname = \"Test\"\nversion = %q\n%s\n", id, version, extraManifest)
	write(t, filepath.Join(dir, "rex-plugin.toml"), manifest)
	for name, content := range files {
		write(t, filepath.Join(dir, name), content)
	}
	return dir
}

const configManifest = `config = "rex/config.lua"`

func configPlugin(t *testing.T, id, version, config string) string {
	t.Helper()
	return pluginFiles(t, id, version, configManifest, map[string]string{"rex/config.lua": config})
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func missing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s should not exist (err = %v)", path, err)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitPlugin makes a git repository holding a plugin and returns its path.
func gitPlugin(t *testing.T, id, version, config string) string {
	t.Helper()
	dir := configPlugin(t, id, version, config)
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "first")
	return dir
}

func commitChange(t *testing.T, repo, version, config string) {
	t.Helper()
	manifest := read(t, filepath.Join(repo, "rex-plugin.toml"))
	lines := strings.Split(manifest, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "version") {
			lines[i] = fmt.Sprintf("version = %q", version)
		}
	}
	write(t, filepath.Join(repo, "rex-plugin.toml"), strings.Join(lines, "\n"))
	write(t, filepath.Join(repo, "rex/config.lua"), config)
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "change "+version)
}

func TestInstallCopy(t *testing.T) {
	e := newEnv(t)
	src := configPlugin(t, "acme.foo", "1.0", "-- config one")
	before := e.fake.Reloads()

	plugin, err := e.m.Install(context.Background(), Source{Kind: state.KindCopy, Location: src})
	if err != nil {
		t.Fatal(err)
	}
	if plugin.ID != "acme.foo" || plugin.Version != "1.0" || plugin.Generation != 1 || plugin.Kind != state.KindCopy {
		t.Errorf("plugin = %+v", plugin)
	}

	if got := read(t, filepath.Join(e.dirs.Plugin("acme.foo"), "rex/config.lua")); got != "-- config one" {
		t.Errorf("plugin file = %q", got)
	}
	if got := read(t, e.layout.Module("acme.foo")); got != "-- config one" {
		t.Errorf("module = %q", got)
	}
	if !strings.Contains(read(t, e.layout.Index()), `load_plugin("acme.foo", "1.0", "`+e.dirs.Plugin("acme.foo")+`")`) {
		t.Errorf("index:\n%s", read(t, e.layout.Index()))
	}
	if saved := e.state().Plugins["acme.foo"]; saved == nil || saved.Source != src {
		t.Errorf("state = %+v", saved)
	}
	if e.fake.Reloads() != before+1 {
		t.Errorf("reloads = %d, was %d", e.fake.Reloads(), before)
	}

	entries, _ := os.ReadDir(e.dirs.Staging())
	if len(entries) != 0 {
		t.Errorf("staging not empty: %v", entries)
	}
}

func TestInstallLink(t *testing.T) {
	e := newEnv(t)
	src := configPlugin(t, "acme.foo", "1.0", "-- one")
	if _, err := e.m.Install(context.Background(), Source{Kind: state.KindLink, Location: src}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(e.dirs.Plugin("acme.foo"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("plugin folder should be a link: %v, %v", info, err)
	}
	write(t, filepath.Join(src, "rex/config.lua"), "-- edited")
	if got := read(t, e.layout.Module("acme.foo")); got != "-- edited" {
		t.Errorf("a linked module should follow the source: %q", got)
	}
}

func TestInstallGit(t *testing.T) {
	e := newEnv(t)
	repo := gitPlugin(t, "acme.foo", "1.0", "-- one")
	head := runGit(t, repo, "rev-parse", "HEAD")
	plugin, err := e.m.Install(context.Background(), Source{Kind: state.KindGit, Location: "file://" + repo})
	if err != nil {
		t.Fatal(err)
	}
	if plugin.Commit != head {
		t.Errorf("commit = %q, want %q", plugin.Commit, head)
	}
	if got := read(t, e.layout.Module("acme.foo")); got != "-- one" {
		t.Errorf("module = %q", got)
	}
}

func TestInstallGitAtARef(t *testing.T) {
	e := newEnv(t)
	repo := gitPlugin(t, "acme.foo", "1.0", "-- one")
	runGit(t, repo, "tag", "v1")
	commitChange(t, repo, "2.0", "-- two")

	plugin, err := e.m.Install(context.Background(), Source{Kind: state.KindGit, Location: "file://" + repo, Ref: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if plugin.Version != "1.0" || plugin.Ref != "v1" {
		t.Errorf("plugin = %+v", plugin)
	}
}

func TestInstallOfAFolderThatIsNotAPluginNamesTheFolder(t *testing.T) {
	e := newEnv(t)
	src := t.TempDir()
	_, err := e.m.Install(context.Background(), Source{Kind: state.KindCopy, Location: src})
	if err == nil || !strings.Contains(err.Error(), src+": ") || !strings.Contains(err.Error(), "not a rexd plugin") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "staging") {
		t.Fatalf("the error names a temporary folder: %v", err)
	}
}

func TestInstallBadGitSource(t *testing.T) {
	e := newEnv(t)
	_, err := e.m.Install(context.Background(), Source{Kind: state.KindGit, Location: "file://" + t.TempDir() + "/nope"})
	if err == nil || !strings.Contains(err.Error(), "git clone") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallRunsBuildSteps(t *testing.T) {
	e := newEnv(t)
	src := pluginFiles(t, "acme.foo", "1.0", configManifest+`
[[build]]
run = ["sh", "-c", "echo $REXD_PLUGIN_ID-$REXD_PLUGIN_VERSION > built.txt"]
`, map[string]string{"rex/config.lua": "-- x"})
	if _, err := e.m.Install(context.Background(), Source{Kind: state.KindCopy, Location: src}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(e.dirs.Plugin("acme.foo"), "built.txt")); got != "acme.foo-1.0\n" {
		t.Errorf("built.txt = %q", got)
	}
	missing(t, filepath.Join(src, "built.txt")) // A copy builds in the copy.
	if !strings.Contains(e.out.String(), "Building: sh -c") {
		t.Errorf("output should name the build step:\n%s", e.out)
	}
}

// A build may embed this path, so it must be where the plugin ends up. The folder
// it builds in is temporary.
func TestBuildStepsSeeTheFinalPluginFolder(t *testing.T) {
	e := newEnv(t)
	src := pluginFiles(t, "acme.foo", "1.0", configManifest+`
[[build]]
run = ["sh", "-c", "echo $REXD_PLUGIN_DIR > dir.txt"]
`, map[string]string{"rex/config.lua": "-- x"})
	if _, err := e.m.Install(context.Background(), Source{Kind: state.KindCopy, Location: src}); err != nil {
		t.Fatal(err)
	}
	want := e.dirs.Plugin("acme.foo") + "\n"
	if got := read(t, filepath.Join(e.dirs.Plugin("acme.foo"), "dir.txt")); got != want {
		t.Errorf("on install: REXD_PLUGIN_DIR = %q, want %q", got, want)
	}

	write(t, filepath.Join(src, "rex/config.lua"), "-- y")
	if _, err := e.m.Update(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(e.dirs.Plugin("acme.foo"), "dir.txt")); got != want {
		t.Errorf("on update: REXD_PLUGIN_DIR = %q, want %q", got, want)
	}
}

func TestInstallFailedBuildChangesNothing(t *testing.T) {
	e := newEnv(t)
	src := pluginFiles(t, "acme.foo", "1.0", configManifest+`
[[build]]
run = ["sh", "-c", "exit 3"]
`, map[string]string{"rex/config.lua": "-- x"})
	indexBefore := read(t, e.layout.Index())
	_, err := e.m.Install(context.Background(), Source{Kind: state.KindCopy, Location: src})
	if err == nil || !strings.Contains(err.Error(), "build step") {
		t.Fatalf("err = %v", err)
	}
	e.assertNotInstalled("acme.foo", indexBefore)
}

func (e *env) assertNotInstalled(id, indexBefore string) {
	e.t.Helper()
	missing(e.t, e.dirs.Plugin(id))
	missing(e.t, e.layout.Module(id))
	if got := read(e.t, e.layout.Index()); got != indexBefore {
		e.t.Errorf("index changed:\n%s", got)
	}
	if _, ok := e.state().Plugins[id]; ok {
		e.t.Errorf("%s is in the state", id)
	}
}

func TestInstallBrokenConfigRollsBack(t *testing.T) {
	e := newEnv(t)
	src := configPlugin(t, "acme.foo", "1.0", "BROKEN")
	indexBefore := read(t, e.layout.Index())
	initBefore := read(t, e.init)
	reloads := e.fake.Reloads()

	_, err := e.m.Install(context.Background(), Source{Kind: state.KindCopy, Location: src})
	if err == nil || !strings.Contains(err.Error(), "did not install") {
		t.Fatalf("err = %v", err)
	}
	e.assertNotInstalled("acme.foo", indexBefore)
	if got := read(t, e.init); got != initBefore {
		t.Errorf("init.lua changed: %q", got)
	}
	if e.fake.Reloads() != reloads {
		t.Error("reloaded Rex for a failed install")
	}
	if _, err := os.Stat(filepath.Join(src, "rex/config.lua")); err != nil {
		t.Errorf("the source was harmed: %v", err)
	}
}

func TestInstallLinkedBrokenConfigKeepsTheSource(t *testing.T) {
	e := newEnv(t)
	src := configPlugin(t, "acme.foo", "1.0", "BROKEN")
	if _, err := e.m.Install(context.Background(), Source{Kind: state.KindLink, Location: src}); err == nil {
		t.Fatal("no error")
	}
	missing(t, e.dirs.Plugin("acme.foo"))
	if _, err := os.Stat(filepath.Join(src, "rex/config.lua")); err != nil {
		t.Fatalf("rolling back deleted the source: %v", err)
	}
}

func TestInstallRefusesWhenTheUsersConfigIsBroken(t *testing.T) {
	e := newEnv(t)
	write(t, e.init, read(t, e.init)+"BROKEN\n")
	src := configPlugin(t, "acme.foo", "1.0", "-- fine")
	_, err := e.m.Install(context.Background(), Source{Kind: state.KindCopy, Location: src})
	if err == nil || !strings.Contains(err.Error(), "your Rex config has errors") {
		t.Fatalf("err = %v", err)
	}
	missing(t, e.dirs.Plugin("acme.foo"))
}

func TestInstallRefusesADuplicate(t *testing.T) {
	e := newEnv(t)
	src := configPlugin(t, "acme.foo", "1.0", "-- x")
	if _, err := e.m.Install(context.Background(), Source{Kind: state.KindCopy, Location: src}); err != nil {
		t.Fatal(err)
	}
	_, err := e.m.Install(context.Background(), Source{Kind: state.KindCopy, Location: src})
	if err == nil || !strings.Contains(err.Error(), "already installed") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallChecksTheAPIRevision(t *testing.T) {
	e := newEnv(t)
	e.fake.SetAPI(7)
	src := pluginFiles(t, "acme.foo", "1.0", configManifest+"\nrex_api = 8", map[string]string{"rex/config.lua": "-- x"})
	_, err := e.m.Install(context.Background(), Source{Kind: state.KindCopy, Location: src})
	if err == nil || !strings.Contains(err.Error(), "needs Rex API revision 8") {
		t.Fatalf("err = %v", err)
	}
	e.fake.SetAPI(8)
	if _, err := e.m.Install(context.Background(), Source{Kind: state.KindCopy, Location: src}); err != nil {
		t.Fatal(err)
	}
}

func TestInstallBeforeSetup(t *testing.T) {
	e := newBareEnv(t)
	src := configPlugin(t, "acme.foo", "1.0", "-- x")
	_, err := e.m.Install(context.Background(), Source{Kind: state.KindCopy, Location: src})
	if !errors.Is(err, ErrNotSetUp) {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallAfterTheRequireLineWasRemoved(t *testing.T) {
	e := newEnv(t)
	write(t, e.init, "-- the user removed the line\n")
	src := configPlugin(t, "acme.foo", "1.0", "-- x")
	_, err := e.m.Install(context.Background(), Source{Kind: state.KindCopy, Location: src})
	if err == nil || !strings.Contains(err.Error(), "no longer loads rexd") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallScriptOnlyPlugin(t *testing.T) {
	e := newEnv(t)
	src := pluginFiles(t, "acme.foo", "1.0", "[[scripts]]\nname = \"watch\"\npath = \"watch.lua\"", map[string]string{"watch.lua": "-- x"})
	before := e.fake.Reloads()
	if _, err := e.m.Install(context.Background(), Source{Kind: state.KindCopy, Location: src}); err != nil {
		t.Fatal(err)
	}
	missing(t, e.layout.Module("acme.foo"))
	if strings.Contains(read(t, e.layout.Index()), "acme.foo") {
		t.Error("a plugin without config code should not be in the index")
	}
	if e.fake.Reloads() != before {
		t.Error("no config changed, so Rex should not reload")
	}
	if !strings.Contains(e.out.String(), "daemon is not running") {
		t.Errorf("should warn that scripts will not start:\n%s", e.out)
	}
}

func TestInstallWithRexDown(t *testing.T) {
	e := newEnv(t)
	e.fake.SetDown(true)
	src := configPlugin(t, "acme.foo", "1.0", "-- x")
	if _, err := e.m.Install(context.Background(), Source{Kind: state.KindCopy, Location: src}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), "Rex is not running") {
		t.Errorf("output:\n%s", e.out)
	}
	if _, ok := e.state().Plugins["acme.foo"]; !ok {
		t.Error("not installed")
	}
}

func TestInstallReplacesALeftoverFolder(t *testing.T) {
	e := newEnv(t)
	write(t, filepath.Join(e.dirs.Plugin("acme.foo"), "leftover.txt"), "from a crash")
	src := configPlugin(t, "acme.foo", "1.0", "-- x")
	if _, err := e.m.Install(context.Background(), Source{Kind: state.KindCopy, Location: src}); err != nil {
		t.Fatal(err)
	}
	missing(t, filepath.Join(e.dirs.Plugin("acme.foo"), "leftover.txt"))
}

func TestWaitingForAnotherCommandSaysSoAndCanBeCancelled(t *testing.T) {
	e := newEnv(t)
	held, err := lockfile.Acquire(context.Background(), e.dirs.StateLock())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	src := configPlugin(t, "acme.foo", "1.0", "-- x")
	_, err = e.m.Install(ctx, Source{Kind: state.KindCopy, Location: src})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(e.out.String(), "Waiting for another rexd command") {
		t.Errorf("output:\n%s", e.out)
	}
	missing(t, e.dirs.Plugin("acme.foo"))
}

func TestRemove(t *testing.T) {
	e := newEnv(t)
	src := configPlugin(t, "acme.foo", "1.0", "-- x")
	if _, err := e.m.Install(context.Background(), Source{Kind: state.KindCopy, Location: src}); err != nil {
		t.Fatal(err)
	}
	logFile := e.dirs.ScriptLog("acme.foo", "watch")
	write(t, logFile, "old output")
	before := e.fake.Reloads()

	if err := e.m.Remove(context.Background(), "acme.foo"); err != nil {
		t.Fatal(err)
	}
	missing(t, e.dirs.Plugin("acme.foo"))
	missing(t, e.layout.Module("acme.foo"))
	missing(t, logFile)
	if strings.Contains(read(t, e.layout.Index()), "acme.foo") {
		t.Error("still in the index")
	}
	if _, ok := e.state().Plugins["acme.foo"]; ok {
		t.Error("still in the state")
	}
	if e.fake.Reloads() != before+1 {
		t.Error("Rex should reload after a remove")
	}
}

func TestRemoveLinkedPluginKeepsTheSource(t *testing.T) {
	e := newEnv(t)
	src := configPlugin(t, "acme.foo", "1.0", "-- x")
	if _, err := e.m.Install(context.Background(), Source{Kind: state.KindLink, Location: src}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Remove(context.Background(), "acme.foo"); err != nil {
		t.Fatal(err)
	}
	missing(t, e.dirs.Plugin("acme.foo"))
	if got := read(t, filepath.Join(src, "rex/config.lua")); got != "-- x" {
		t.Fatalf("source = %q", got)
	}
}

func TestRemoveUnknownPlugin(t *testing.T) {
	e := newEnv(t)
	err := e.m.Remove(context.Background(), "acme.nope")
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateGit(t *testing.T) {
	e := newEnv(t)
	repo := gitPlugin(t, "acme.foo", "1.0", "-- one")
	if _, err := e.m.Install(context.Background(), Source{Kind: state.KindGit, Location: "file://" + repo}); err != nil {
		t.Fatal(err)
	}
	commitChange(t, repo, "2.0", "-- two")
	before := e.fake.Reloads()

	results, err := e.m.Update(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Changed || results[0].From != "1.0" || results[0].To != "2.0" {
		t.Fatalf("results = %+v", results)
	}
	saved := e.state().Plugins["acme.foo"]
	if saved.Version != "2.0" || saved.Generation != 2 || saved.Commit != runGit(t, repo, "rev-parse", "HEAD") {
		t.Errorf("state = %+v", saved)
	}
	if got := read(t, e.layout.Module("acme.foo")); got != "-- two" {
		t.Errorf("module = %q", got)
	}
	if got := read(t, filepath.Join(e.dirs.Plugin("acme.foo"), "rex/config.lua")); got != "-- two" {
		t.Errorf("plugin file = %q", got)
	}
	if !strings.Contains(read(t, e.layout.Index()), `"2.0"`) {
		t.Error("index still has the old version")
	}
	if e.fake.Reloads() != before+1 {
		t.Error("Rex should reload after an update")
	}
}

func TestUpdateGitWithoutChanges(t *testing.T) {
	e := newEnv(t)
	repo := gitPlugin(t, "acme.foo", "1.0", "-- one")
	if _, err := e.m.Install(context.Background(), Source{Kind: state.KindGit, Location: "file://" + repo}); err != nil {
		t.Fatal(err)
	}
	before := e.fake.Reloads()
	results, err := e.m.Update(context.Background(), []string{"acme.foo"})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Changed || results[0].Note != "already up to date" {
		t.Fatalf("results = %+v", results)
	}
	if e.state().Plugins["acme.foo"].Generation != 1 {
		t.Error("generation went up without a change")
	}
	if e.fake.Reloads() != before {
		t.Error("reloaded without a change")
	}
}

func TestUpdateLeavesAPinnedPluginAlone(t *testing.T) {
	e := newEnv(t)
	repo := gitPlugin(t, "acme.foo", "1.0", "-- one")
	runGit(t, repo, "tag", "v1")
	if _, err := e.m.Install(context.Background(), Source{Kind: state.KindGit, Location: "file://" + repo, Ref: "v1"}); err != nil {
		t.Fatal(err)
	}
	commitChange(t, repo, "2.0", "-- two")
	results, err := e.m.Update(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Changed || !strings.Contains(results[0].Note, "pinned to v1") {
		t.Fatalf("results = %+v", results)
	}
	if got := read(t, e.layout.Module("acme.foo")); got != "-- one" {
		t.Errorf("module = %q", got)
	}
}

func TestUpdateReportsAPluginGitCannotRead(t *testing.T) {
	e := newEnv(t)
	repo := gitPlugin(t, "acme.foo", "1.0", "-- one")
	if _, err := e.m.Install(context.Background(), Source{Kind: state.KindGit, Location: "file://" + repo}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(e.dirs.Plugin("acme.foo"), ".git")); err != nil {
		t.Fatal(err)
	}
	results, err := e.m.Update(context.Background(), nil)
	if err == nil {
		t.Fatalf("no error, results = %+v", results)
	}
}

func TestUpdateBrokenVersionKeepsTheOldOne(t *testing.T) {
	e := newEnv(t)
	repo := gitPlugin(t, "acme.foo", "1.0", "-- one")
	if _, err := e.m.Install(context.Background(), Source{Kind: state.KindGit, Location: "file://" + repo}); err != nil {
		t.Fatal(err)
	}
	commitChange(t, repo, "2.0", "BROKEN")
	indexBefore := read(t, e.layout.Index())
	stateBefore := read(t, e.dirs.StateFile())

	_, err := e.m.Update(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "old version stays") {
		t.Fatalf("err = %v", err)
	}
	if got := read(t, e.layout.Module("acme.foo")); got != "-- one" {
		t.Errorf("module = %q", got)
	}
	if got := read(t, filepath.Join(e.dirs.Plugin("acme.foo"), "rex/config.lua")); got != "-- one" {
		t.Errorf("plugin file = %q", got)
	}
	if got := read(t, e.layout.Index()); got != indexBefore {
		t.Errorf("index changed:\n%s", got)
	}
	if got := read(t, e.dirs.StateFile()); got != stateBefore {
		t.Errorf("state changed:\n%s", got)
	}
}

// The last check can fail after the config code has been replaced. Rollback must
// then leave rexd.lua in step with what is back in place.
func TestUpdateRollbackWhenTheNewVersionDropsConfigCode(t *testing.T) {
	e := newEnv(t)
	src := pluginFiles(t, "acme.foo", "1.0", configManifest+"\n[[scripts]]\nname = \"s\"\npath = \"s.lua\"", map[string]string{"rex/config.lua": "-- x", "s.lua": ""})
	if _, err := e.m.Install(context.Background(), Source{Kind: state.KindCopy, Location: src}); err != nil {
		t.Fatal(err)
	}
	indexBefore := read(t, e.layout.Index())
	write(t, filepath.Join(src, "rex-plugin.toml"), "id = \"acme.foo\"\nname = \"T\"\nversion = \"2\"\n[[scripts]]\nname = \"s\"\npath = \"s.lua\"\n")

	e.fake.FailChecksAfter(1) // The check of the user's config passes, and the last one fails.
	if _, err := e.m.Update(context.Background(), nil); err == nil {
		t.Fatal("no error")
	}

	if got := read(t, e.layout.Module("acme.foo")); got != "-- x" {
		t.Errorf("module = %q", got)
	}
	if got := read(t, e.layout.Index()); got != indexBefore {
		t.Errorf("index should list the plugin again:\n%s", got)
	}
	if got := e.state().Plugins["acme.foo"]; got.Version != "1.0" || got.Generation != 1 {
		t.Errorf("state = %+v", got)
	}
}

func TestUpdateRollbackWhenTheNewVersionAddsConfigCode(t *testing.T) {
	e := newEnv(t)
	src := pluginFiles(t, "acme.foo", "1.0", "[[scripts]]\nname = \"s\"\npath = \"s.lua\"", map[string]string{"s.lua": "", "rex/config.lua": "-- x"})
	if _, err := e.m.Install(context.Background(), Source{Kind: state.KindCopy, Location: src}); err != nil {
		t.Fatal(err)
	}
	indexBefore := read(t, e.layout.Index())
	write(t, filepath.Join(src, "rex-plugin.toml"), "id = \"acme.foo\"\nname = \"T\"\nversion = \"2\"\n"+configManifest+"\n[[scripts]]\nname = \"s\"\npath = \"s.lua\"\n")

	e.fake.FailChecksAfter(2) // The user's config and the plugin pass, and the last check fails.
	if _, err := e.m.Update(context.Background(), nil); err == nil {
		t.Fatal("no error")
	}

	missing(t, e.layout.Module("acme.foo"))
	if got := read(t, e.layout.Index()); got != indexBefore {
		t.Errorf("index should not list the plugin:\n%s", got)
	}
}

func TestUpdateOneFailureDoesNotStopTheOthers(t *testing.T) {
	e := newEnv(t)
	bad := gitPlugin(t, "acme.bad", "1.0", "-- one")
	good := gitPlugin(t, "acme.good", "1.0", "-- one")
	for _, repo := range []string{bad, good} {
		if _, err := e.m.Install(context.Background(), Source{Kind: state.KindGit, Location: "file://" + repo}); err != nil {
			t.Fatal(err)
		}
	}
	commitChange(t, bad, "2.0", "BROKEN")
	commitChange(t, good, "2.0", "-- two")

	results, err := e.m.Update(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "acme.bad") {
		t.Fatalf("err = %v", err)
	}
	if len(results) != 1 || results[0].ID != "acme.good" || !results[0].Changed {
		t.Fatalf("results = %+v", results)
	}
	saved := e.state()
	if saved.Plugins["acme.good"].Version != "2.0" || saved.Plugins["acme.bad"].Version != "1.0" {
		t.Errorf("versions: good %s, bad %s", saved.Plugins["acme.good"].Version, saved.Plugins["acme.bad"].Version)
	}
}

func TestUpdateRefusesAChangedID(t *testing.T) {
	e := newEnv(t)
	repo := gitPlugin(t, "acme.foo", "1.0", "-- one")
	if _, err := e.m.Install(context.Background(), Source{Kind: state.KindGit, Location: "file://" + repo}); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, "rex-plugin.toml"), "id = \"acme.renamed\"\nname = \"T\"\nversion = \"2\"\nconfig = \"rex/config.lua\"\n")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "rename")
	_, err := e.m.Update(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "its id changed") {
		t.Fatalf("err = %v", err)
	}
	if e.state().Plugins["acme.foo"].Version != "1.0" {
		t.Error("version changed")
	}
}

func TestUpdateLink(t *testing.T) {
	e := newEnv(t)
	src := pluginFiles(t, "acme.foo", "1.0", configManifest+`
[[build]]
run = ["sh", "-c", "date > built.txt"]
`, map[string]string{"rex/config.lua": "-- one"})
	if _, err := e.m.Install(context.Background(), Source{Kind: state.KindLink, Location: src}); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(src, "built.txt"))
	write(t, filepath.Join(src, "rex/config.lua"), "-- two")

	results, err := e.m.Update(context.Background(), []string{"acme.foo"})
	if err != nil {
		t.Fatal(err)
	}
	if !results[0].Changed {
		t.Fatalf("results = %+v", results)
	}
	if e.state().Plugins["acme.foo"].Generation != 2 {
		t.Error("generation should go up, so the daemon restarts the scripts")
	}
	if _, err := os.Stat(filepath.Join(src, "built.txt")); err != nil {
		t.Errorf("the build should run again: %v", err)
	}
	if _, err := os.Lstat(e.dirs.Plugin("acme.foo")); err != nil {
		t.Errorf("the link is gone: %v", err)
	}
}

func TestUpdateCopy(t *testing.T) {
	e := newEnv(t)
	src := configPlugin(t, "acme.foo", "1.0", "-- one")
	if _, err := e.m.Install(context.Background(), Source{Kind: state.KindCopy, Location: src}); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(src, "rex/config.lua"), "-- two")
	if _, err := e.m.Update(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := read(t, e.layout.Module("acme.foo")); got != "-- two" {
		t.Errorf("module = %q", got)
	}
}

func TestUpdateUnknownPlugin(t *testing.T) {
	e := newEnv(t)
	_, err := e.m.Update(context.Background(), []string{"acme.nope"})
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateRemovingConfigCodeRemovesTheModule(t *testing.T) {
	e := newEnv(t)
	src := pluginFiles(t, "acme.foo", "1.0", configManifest+"\n[[scripts]]\nname = \"s\"\npath = \"s.lua\"", map[string]string{"rex/config.lua": "-- x", "s.lua": ""})
	if _, err := e.m.Install(context.Background(), Source{Kind: state.KindCopy, Location: src}); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(src, "rex-plugin.toml"), "id = \"acme.foo\"\nname = \"T\"\nversion = \"2\"\n[[scripts]]\nname = \"s\"\npath = \"s.lua\"\n")
	before := e.fake.Reloads()
	if _, err := e.m.Update(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	missing(t, e.layout.Module("acme.foo"))
	if strings.Contains(read(t, e.layout.Index()), "acme.foo") {
		t.Error("still in the index")
	}
	if e.fake.Reloads() != before+1 {
		t.Error("Rex must reload, or the old action stays live")
	}
}
