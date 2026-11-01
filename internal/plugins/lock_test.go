package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tornikegomareli/rexd/internal/state"
)

func readLock(t *testing.T, e *env) lockFile {
	t.Helper()
	var lock lockFile
	if err := json.Unmarshal([]byte(read(t, e.layout.Lock())), &lock); err != nil {
		t.Fatal(err)
	}
	return lock
}

func installGit(t *testing.T, e *env, repo, ref string) *state.Plugin {
	t.Helper()
	p, err := e.m.Install(context.Background(), Source{Kind: state.KindGit, Location: "file://" + repo, Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTheLockFollowsEveryChange(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	repo := gitPlugin(t, "a.b", "1.0", "-- first\n")
	p := installGit(t, e, repo, "")
	got := readLock(t, e).Plugins["a.b"]
	if got.Commit != p.Commit || got.Source != "file://"+repo {
		t.Fatalf("after install: %+v", got)
	}

	commitChange(t, repo, "2.0", "-- second\n")
	if _, err := e.m.Update(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got := readLock(t, e).Plugins["a.b"]; got.Commit == p.Commit || got.Commit != e.state().Plugins["a.b"].Commit {
		t.Fatalf("after update: %+v", got)
	}

	if err := e.m.SetOptions(ctx, "a.b", map[string]any{"key": "x"}); err != nil {
		t.Fatal(err)
	}
	if got := readLock(t, e).Plugins["a.b"]; got.Options["key"] != "x" {
		t.Fatalf("after set: %+v", got)
	}

	if err := e.m.Remove(ctx, "a.b"); err != nil {
		t.Fatal(err)
	}
	if fileExists(e.layout.Lock()) {
		t.Fatal("the lock stays with nothing in it")
	}
}

func TestLocalPluginsStayOutOfTheLock(t *testing.T) {
	e := newEnv(t)
	mustInstall(t, e, configPlugin(t, "a.b", "1.0", "-- config\n"))
	if fileExists(e.layout.Lock()) {
		t.Fatalf("lock:\n%s", read(t, e.layout.Lock()))
	}
}

func TestTheLockWritesThroughALink(t *testing.T) {
	e := newEnv(t)
	real := filepath.Join(t.TempDir(), "rexd.lock")
	write(t, real, "{}")
	if err := os.Symlink(real, e.layout.Lock()); err != nil {
		t.Fatal(err)
	}
	installGit(t, e, gitPlugin(t, "a.b", "1.0", "-- first\n"), "")
	if info, err := os.Lstat(e.layout.Lock()); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the link was replaced")
	}
	if !strings.Contains(read(t, real), "a.b") {
		t.Fatalf("real file:\n%s", read(t, real))
	}
}

func TestSyncInstallsTheLockedCommitOnAnotherMachine(t *testing.T) {
	first := newEnv(t)
	ctx := context.Background()
	repo := gitPlugin(t, "a.b", "1.0", "-- first\n")
	locked := installGit(t, first, repo, "")
	if err := first.m.SetOptions(ctx, "a.b", map[string]any{"key": "x"}); err != nil {
		t.Fatal(err)
	}
	commitChange(t, repo, "2.0", "-- newer\n")

	second := newEnv(t)
	write(t, second.layout.Lock(), read(t, first.layout.Lock()))
	results, err := second.m.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Changed {
		t.Fatalf("%+v", results)
	}
	p := second.state().Plugins["a.b"]
	if p.Commit != locked.Commit || p.Version != "1.0" || p.Options["key"] != "x" {
		t.Fatalf("%+v", p)
	}
	if got := read(t, second.layout.Index()); !strings.Contains(got, `["key"] = "x"`) {
		t.Fatalf("index:\n%s", got)
	}

	// It still follows its branch afterwards.
	updates, err := second.m.Update(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !updates[0].Changed || updates[0].To != "2.0" {
		t.Fatalf("%+v", updates)
	}
}

func TestSyncMovesAPluginToTheLockedCommit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	repo := gitPlugin(t, "a.b", "1.0", "-- first\n")
	first := installGit(t, e, repo, "").Commit
	lock := read(t, e.layout.Lock())
	commitChange(t, repo, "2.0", "-- second\n")
	if _, err := e.m.Update(ctx, nil); err != nil {
		t.Fatal(err)
	}

	write(t, e.layout.Lock(), lock)
	results, err := e.m.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !results[0].Changed {
		t.Fatalf("%+v", results)
	}
	if p := e.state().Plugins["a.b"]; p.Commit != first || p.Version != "1.0" {
		t.Fatalf("%+v", p)
	}
	if got := read(t, e.layout.Lock()); got != lock {
		t.Errorf("lock changed:\n%s", got)
	}

	again, err := e.m.Sync(ctx)
	if err != nil || again[0].Changed {
		t.Fatalf("second sync: %+v, %v", again, err)
	}
}

func TestSyncAtARangeKeepsTheRange(t *testing.T) {
	first := newEnv(t)
	ctx := context.Background()
	repo := gitPlugin(t, "a.b", "1.0.0", "-- first\n")
	runGit(t, repo, "tag", "v1.0.0")
	locked := installGit(t, first, repo, "^1.0.0")
	tagged(t, repo, "1.1.0")

	second := newEnv(t)
	write(t, second.layout.Lock(), read(t, first.layout.Lock()))
	if _, err := second.m.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	p := second.state().Plugins["a.b"]
	if p.Commit != locked.Commit || p.Ref != "^1.0.0" {
		t.Fatalf("%+v", p)
	}
	updates, err := second.m.Update(ctx, nil)
	if err != nil || !updates[0].Changed || updates[0].To != "1.1.0" {
		t.Fatalf("%+v, %v", updates, err)
	}
}

func TestAFailedSyncKeepsTheLock(t *testing.T) {
	e := newEnv(t)
	good := gitPlugin(t, "a.good", "1.0", "-- good\n")
	gone := gitPlugin(t, "b.gone", "1.0", "-- gone\n")
	installGit(t, e, good, "")
	installGit(t, e, gone, "")
	lock := read(t, e.layout.Lock())

	second := newEnv(t)
	write(t, second.layout.Lock(), lock)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	results, err := second.m.Sync(context.Background())
	if err == nil || !strings.Contains(err.Error(), "b.gone") {
		t.Fatalf("err %v", err)
	}
	if len(results) != 1 || results[0].ID != "a.good" {
		t.Fatalf("%+v", results)
	}
	if got := read(t, second.layout.Lock()); got != lock {
		t.Errorf("lock changed:\n%s", got)
	}
}

func TestSyncNamesWhatTheLockDoesNotHold(t *testing.T) {
	e := newEnv(t)
	a := gitPlugin(t, "a.a", "1.0", "-- a\n")
	installGit(t, e, a, "")
	lock := read(t, e.layout.Lock())
	installGit(t, e, gitPlugin(t, "b.b", "1.0", "-- b\n"), "")
	write(t, e.layout.Lock(), lock)

	results, err := e.m.Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[1].ID != "b.b" || !strings.Contains(results[1].Note, "not in the lock") {
		t.Fatalf("%+v", results)
	}
	if _, ok := e.state().Plugins["b.b"]; !ok {
		t.Fatal("sync removed a plugin")
	}
}

func TestSyncWithNoLockExplainsItself(t *testing.T) {
	e := newEnv(t)
	if _, err := e.m.Sync(context.Background()); err == nil || !strings.Contains(err.Error(), "rexd.lock") {
		t.Fatalf("err %v", err)
	}
}

func TestPurgeRemovesTheLock(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	installGit(t, e, gitPlugin(t, "a.b", "1.0", "-- first\n"), "")
	if err := e.m.Teardown(ctx, TeardownOptions{}); err != nil {
		t.Fatal(err)
	}
	if !fileExists(e.layout.Lock()) {
		t.Fatal("plain teardown removed the lock")
	}
	if _, err := e.m.Setup(ctx, SetupOptions{RexConfig: e.init}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Teardown(ctx, TeardownOptions{Purge: true}); err != nil {
		t.Fatal(err)
	}
	if fileExists(e.layout.Lock()) {
		t.Fatal("purge kept the lock")
	}
}
