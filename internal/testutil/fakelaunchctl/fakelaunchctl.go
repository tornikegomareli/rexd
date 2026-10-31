// Package fakelaunchctl puts a stand-in for launchctl first on PATH, so a test can
// reach the code that talks to launchd without touching launchd.
package fakelaunchctl

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The script keeps one file for each loaded label, and it does not start anything.
const script = `#!/bin/sh
dir=%q
echo "$*" >> "$dir/calls"
case "$1" in
print) test -e "$dir/loaded/${2##*/}" ;;
bootout) /bin/rm -f "$dir/loaded/${2##*/}" ;;
bootstrap) file="${3##*/}"; : > "$dir/loaded/${file%%.plist}" ;;
esac
`

// Fake is a launchctl with its own list of loaded agents.
type Fake struct {
	// Dir holds the program, for a test that sets its own PATH.
	Dir string
}

// New installs the fake for the length of the test.
func New(t testing.TB) *Fake {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "loaded"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "launchctl"), fmt.Appendf(nil, script, dir), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &Fake{Dir: dir}
}

// Load makes launchd know an agent.
func (f *Fake) Load(t testing.TB, label string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.Dir, "loaded", label), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Loaded reports whether launchd knows an agent.
func (f *Fake) Loaded(label string) bool {
	_, err := os.Stat(filepath.Join(f.Dir, "loaded", label))
	return err == nil
}

// Calls returns the arguments of each launchctl command, the oldest first.
func (f *Fake) Calls(t testing.TB) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.Dir, "calls"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}
