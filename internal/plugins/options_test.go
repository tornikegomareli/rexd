package plugins

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tornikegomareli/rexd/internal/rex"
	"github.com/tornikegomareli/rexd/internal/state"
)

func mustInstall(t *testing.T, e *env, src string) {
	t.Helper()
	if _, err := e.m.Install(context.Background(), Source{Kind: state.KindCopy, Location: src}); err != nil {
		t.Fatal(err)
	}
}

func TestParseOptionTypesAValue(t *testing.T) {
	for input, want := range map[string]any{
		"a=1":      float64(1),
		"a=1.5":    1.5,
		"a=true":   true,
		"a=false":  false,
		"a=ctrl+x": "ctrl+x",
		"a=b=c":    "b=c",
		`a=`:       nil,
	} {
		key, value, err := ParseOption(input)
		if err != nil || key != "a" || !reflect.DeepEqual(value, want) {
			t.Errorf("%s: key %q, value %#v, err %v", input, key, value, err)
		}
	}
	for _, bad := range []string{"", "a", "=b", "a b=c"} {
		if _, _, err := ParseOption(bad); err == nil {
			t.Errorf("%q has no error", bad)
		}
	}
}

func TestSetOptionsReachRexAndSurviveAnUpdate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := configPlugin(t, "a.b", "1.0", "-- config\n")
	mustInstall(t, e, src)

	if err := e.m.SetOptions(ctx, "a.b", map[string]any{"key": "cmd+k", "size": float64(3)}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, e.layout.Index()); !strings.Contains(got, `["key"] = "cmd+k"`) || !strings.Contains(got, `["size"] = 3`) {
		t.Fatalf("index:\n%s", got)
	}
	before := e.state().Plugins["a.b"].Generation
	if err := e.m.SetOptions(ctx, "a.b", map[string]any{"size": nil}); err != nil {
		t.Fatal(err)
	}
	p := e.state().Plugins["a.b"]
	if !reflect.DeepEqual(p.Options, map[string]any{"key": "cmd+k"}) {
		t.Fatalf("options %#v", p.Options)
	}
	if p.Generation != before+1 {
		t.Errorf("generation %d, want %d, so scripts restart with the new options", p.Generation, before+1)
	}

	write(t, src+"/rex/config.lua", "-- changed\n")
	if _, err := e.m.Update(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got := e.state().Plugins["a.b"].Options; got["key"] != "cmd+k" {
		t.Fatalf("after update: %#v", got)
	}
	if got := read(t, e.layout.Index()); !strings.Contains(got, `["key"] = "cmd+k"`) {
		t.Fatalf("index after update:\n%s", got)
	}
}

func TestOptionsThatBreakAPluginAreUndone(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	mustInstall(t, e, configPlugin(t, "a.b", "1.0", "-- config\n"))
	indexBefore := read(t, e.layout.Index())

	e.fake.FailChecksAfter(0)
	err := e.m.SetOptions(ctx, "a.b", map[string]any{"key": "nope"})
	var configErr *rex.ConfigError
	if !errors.As(err, &configErr) {
		t.Fatalf("err %v", err)
	}
	if got := e.state().Plugins["a.b"].Options; len(got) != 0 {
		t.Errorf("options kept: %#v", got)
	}
	if got := read(t, e.layout.Index()); got != indexBefore {
		t.Errorf("index changed:\n%s", got)
	}
}

func TestSetOptionsNeedsAnInstalledPlugin(t *testing.T) {
	e := newEnv(t)
	if err := e.m.SetOptions(context.Background(), "a.b", map[string]any{"k": "v"}); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("err %v", err)
	}
}
