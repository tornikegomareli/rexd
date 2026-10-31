package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultUsesXDG(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/x/data")
	t.Setenv("XDG_STATE_HOME", "/x/state")
	d, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if d.Data != "/x/data/rexd" || d.State != "/x/state/rexd" {
		t.Fatalf("got %+v", d)
	}
}

func TestDefaultFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "relative/paths/are/ignored")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	d, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".local/share/rexd"); d.Data != want {
		t.Errorf("Data = %q, want %q", d.Data, want)
	}
	if want := filepath.Join(home, ".local/state/rexd"); d.State != want {
		t.Errorf("State = %q, want %q", d.State, want)
	}
}

func TestLayout(t *testing.T) {
	d := Dirs{Data: "/d", State: "/s"}
	for got, want := range map[string]string{
		d.Plugin("acme.probe"):              "/d/plugins/acme.probe",
		d.Staging():                         "/d/staging",
		d.StateFile():                       "/s/state.json",
		d.StatusFile():                      "/s/status.json",
		d.DaemonLog():                       "/s/logs/daemon.log",
		d.ScriptLog("acme.probe", "notify"): "/s/logs/acme.probe/notify.log",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}
