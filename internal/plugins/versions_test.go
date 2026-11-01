package plugins

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tornikegomareli/rexd/internal/state"
)

func TestBestTag(t *testing.T) {
	tags := []string{"v1.2.0", "v1.2.9", "1.3.0", "v1.10.1", "v2.0.0", "v1.11.0-rc1", "nightly", "v0.2.1", "v0.2.9", "v0.3.0"}
	for ref, want := range map[string]string{
		"^1.2.0": "v1.10.1",
		"~1.2.0": "v1.2.9",
		"^1.3.0": "v1.10.1",
		"~1.3.0": "1.3.0",
		"^0.2.0": "v0.2.9", // Below 1.0, a new minor version can break things.
		"^2.0.0": "v2.0.0",
	} {
		if got, err := bestTag(tags, ref); err != nil || got != want {
			t.Errorf("%s: got %q, %v, want %q", ref, got, err, want)
		}
	}
	if _, err := bestTag(tags, "^3.0.0"); err == nil || !strings.Contains(err.Error(), "^3.0.0") {
		t.Errorf("no match: %v", err)
	}
}

func TestIsRange(t *testing.T) {
	for ref, want := range map[string]bool{"^1.2.3": true, "~1.2.3": true, "^1.2": false, "main": false, "v1.2.3": false, "": false} {
		if got := isRange(ref); got != want {
			t.Errorf("%q: %v", ref, got)
		}
	}
}

func tagged(t *testing.T, repo, version string) {
	t.Helper()
	commitChange(t, repo, version, "-- "+version+"\n")
	runGit(t, repo, "tag", "v"+version)
}

func TestARangeInstallsAndUpdatesWithinItself(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	repo := gitPlugin(t, "a.b", "1.0.0", "-- first\n")
	runGit(t, repo, "tag", "v1.0.0")
	tagged(t, repo, "1.1.0")
	tagged(t, repo, "2.0.0")

	p, err := e.m.Install(ctx, Source{Kind: state.KindGit, Location: "file://" + repo, Ref: "^1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != "1.1.0" || p.Ref != "^1.0.0" {
		t.Fatalf("%+v", p)
	}

	results, err := e.m.Update(ctx, nil)
	if err != nil || results[0].Changed {
		t.Fatalf("nothing new in range: %+v, %v", results, err)
	}

	tagged(t, repo, "1.2.0")
	tagged(t, repo, "2.1.0")
	results, err = e.m.Update(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !results[0].Changed || results[0].To != "1.2.0" {
		t.Fatalf("%+v", results)
	}
	if got := read(t, filepath.Join(e.dirs.Plugin("a.b"), "rex/config.lua")); !strings.Contains(got, "1.2.0") {
		t.Fatalf("config %q", got)
	}
}

func TestARangeWithNoMatchInstallsNothing(t *testing.T) {
	e := newEnv(t)
	repo := gitPlugin(t, "a.b", "1.0.0", "-- first\n")
	runGit(t, repo, "tag", "v1.0.0")
	_, err := e.m.Install(context.Background(), Source{Kind: state.KindGit, Location: "file://" + repo, Ref: "^9.0.0"})
	if err == nil || !strings.Contains(err.Error(), "^9.0.0") {
		t.Fatalf("err %v", err)
	}
	if len(e.state().Plugins) != 0 {
		t.Fatal("installed")
	}
}
