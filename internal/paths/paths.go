// Package paths says where rexd keeps its files.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// Dirs holds the two folders rexd owns.
type Dirs struct {
	// Data holds installed plugins. It is $XDG_DATA_HOME/rexd, or ~/.local/share/rexd.
	Data string
	// State holds rexd's state, the daemon's status, and logs.
	// It is $XDG_STATE_HOME/rexd, or ~/.local/state/rexd.
	State string
}

// Default resolves the folders from the environment.
func Default() (Dirs, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Dirs{}, fmt.Errorf("finding the home folder: %w", err)
	}
	return Dirs{
		Data:  filepath.Join(xdg("XDG_DATA_HOME", filepath.Join(home, ".local", "share")), "rexd"),
		State: filepath.Join(xdg("XDG_STATE_HOME", filepath.Join(home, ".local", "state")), "rexd"),
	}, nil
}

// xdg returns the folder in env, or fallback. The XDG spec says to ignore relative paths.
func xdg(env, fallback string) string {
	if v := os.Getenv(env); filepath.IsAbs(v) {
		return v
	}
	return fallback
}

// Plugins is the folder that holds one folder per installed plugin.
func (d Dirs) Plugins() string { return filepath.Join(d.Data, "plugins") }

// Plugin is the folder of the plugin with this id.
func (d Dirs) Plugin(id string) string { return filepath.Join(d.Plugins(), id) }

// Staging holds plugins while they are fetched and built. It is on the same
// file system as Plugins, so a finished plugin moves into place with a rename.
func (d Dirs) Staging() string { return filepath.Join(d.Data, "staging") }

// StateFile is the list of installed plugins and rexd's settings.
func (d Dirs) StateFile() string { return filepath.Join(d.State, "state.json") }

// StateLock serializes commands that change the state.
func (d Dirs) StateLock() string { return filepath.Join(d.State, "state.lock") }

// StatusFile is where the daemon reports what is running.
func (d Dirs) StatusFile() string { return filepath.Join(d.State, "status.json") }

// DaemonLock is held by the daemon for as long as it runs.
func (d Dirs) DaemonLock() string { return filepath.Join(d.State, "daemon.lock") }

// DaemonPID holds the daemon's process ID.
func (d Dirs) DaemonPID() string { return filepath.Join(d.State, "daemon.pid") }

// Logs is the folder that holds all log files.
func (d Dirs) Logs() string { return filepath.Join(d.State, "logs") }

// DaemonLog is the daemon's own log.
func (d Dirs) DaemonLog() string { return filepath.Join(d.Logs(), "daemon.log") }

// ScriptLog is the output of one event script.
func (d Dirs) ScriptLog(plugin, script string) string {
	return filepath.Join(d.Logs(), plugin, script+".log")
}
