package installation

import (
	"testing"

	"github.com/tornikegomareli/rexd/internal/paths"
	"github.com/tornikegomareli/rexd/internal/service"
)

func TestLabel(t *testing.T) {
	const home = "/Users/me"
	normal := paths.Under(home)
	if got := labelFor(normal, home); got != service.Label {
		t.Errorf("default folders: %q", got)
	}

	other := paths.Dirs{Data: "/tmp/a/rexd", State: "/tmp/b/rexd"}
	label := labelFor(other, home)
	if label == service.Label || label != labelFor(other, home) {
		t.Errorf("other folders: %q", label)
	}
	for _, dirs := range []paths.Dirs{
		{Data: "/tmp/a/rexd", State: normal.State},
		{Data: normal.Data, State: "/tmp/b/rexd"},
		{Data: "/tmp/b/rexd", State: "/tmp/a/rexd"},
	} {
		if got := labelFor(dirs, home); got == label || got == service.Label {
			t.Errorf("%+v: %q", dirs, got)
		}
	}

	// Another $HOME has default folders of its own, but launchd is the same.
	if got := labelFor(paths.Under("/tmp/fake-home"), home); got == service.Label {
		t.Errorf("another home: %q", got)
	}
}

func TestLabelAsksTheAccountAndNotHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", t.TempDir())
	dirs, err := paths.Default()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Label(dirs); err != nil || got == service.Label {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestWithoutVersionedRex(t *testing.T) {
	rexTerminal := "/Users/me/Library/Application Support/com.superlogical.rex/RexServer/Versions/1642/Rex Server.app/Contents/MacOS"
	got := withoutVersionedRex("/opt/homebrew/bin:" + rexTerminal + ":/usr/bin")
	if got != "/opt/homebrew/bin:/usr/bin" {
		t.Fatalf("got %q", got)
	}
	if got := withoutVersionedRex("/usr/bin:/bin"); got != "/usr/bin:/bin" {
		t.Fatalf("got %q", got)
	}
}

// launchd gives an agent the account's home, so another $HOME must travel in the agent.
func TestServiceSpecKeepsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	spec, err := (&Installation{Dirs: paths.Under(home)}).serviceSpec()
	if err != nil {
		t.Fatal(err)
	}
	if spec.Env["HOME"] != home {
		t.Errorf("env = %v", spec.Env)
	}
}
