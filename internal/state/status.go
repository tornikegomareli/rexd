package state

import (
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/tornikegomareli/rexd/internal/fsutil"
)

const statusVersion = 1

// RexState is what the daemon last learned about the Rex server.
type RexState string

const (
	// RexUnknown means the daemon has not asked yet.
	RexUnknown RexState = ""
	// RexUp means the server answers and has at least one session.
	RexUp RexState = "up"
	// RexNoSessions means the server answers but has no sessions. A script
	// cannot start without one.
	RexNoSessions RexState = "no-sessions"
	// RexDown means the server does not answer.
	RexDown RexState = "down"
)

// ScriptState is where an event script is in its life cycle.
type ScriptState string

const (
	// ScriptWaiting means the script waits for Rex or for a first session.
	ScriptWaiting ScriptState = "waiting"
	// ScriptRunning means the script's process is running.
	ScriptRunning ScriptState = "running"
	// ScriptBackoff means the script exited and a restart is scheduled.
	ScriptBackoff ScriptState = "backoff"
	// ScriptCrashing means the script keeps exiting. Restarts continue at the longest delay.
	ScriptCrashing ScriptState = "crashing"
	// ScriptStopping means the daemon asked the script to stop.
	ScriptStopping ScriptState = "stopping"
)

// Status is the content of status.json.
type Status struct {
	Version int          `json:"version"`
	Daemon  DaemonStatus `json:"daemon"`
	// Scripts are ordered by plugin id, then script name.
	Scripts []ScriptStatus `json:"scripts"`
	// PluginErrors maps a plugin id to why the daemon could not read it.
	PluginErrors map[string]string `json:"plugin_errors,omitempty"`
}

// DaemonStatus describes the daemon that wrote the file.
type DaemonStatus struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Rex       RexState  `json:"rex"`
}

// ScriptStatus describes one event script.
type ScriptStatus struct {
	Plugin string      `json:"plugin"`
	Script string      `json:"script"`
	State  ScriptState `json:"state"`
	// PID is set while the process runs.
	PID int `json:"pid,omitempty"`
	// Since is when the script entered its state.
	Since time.Time `json:"since,omitzero"`
	// Restarts counts starts after the first. It stays at zero when a script
	// only restarted because Rex came back.
	Restarts int `json:"restarts"`
	// NextStart is when a script in backoff starts again.
	NextStart time.Time `json:"next_start,omitzero"`
	// LastExit describes the most recent unexpected exit.
	LastExit *Exit `json:"last_exit,omitempty"`
}

// Exit describes how a script's process ended.
type Exit struct {
	// Code is the exit status, or -1 if a signal ended the process.
	Code int `json:"code"`
	// Error describes the failure, if the process could not start or was signaled.
	Error string    `json:"error,omitempty"`
	At    time.Time `json:"at"`
	// Ran is how long the process ran.
	Ran string `json:"ran"`
}

// LoadStatus reads the status file at path. A missing file returns a nil status.
func LoadStatus(path string) (*Status, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Save writes the status to path atomically.
func (s *Status) Save(path string) error {
	s.Version = statusVersion
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, append(data, '\n'), 0o644)
}
