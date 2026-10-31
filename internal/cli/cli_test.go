package cli

import (
	"bytes"
	"strings"
	"testing"
)

func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = Main(args, Options{Version: "v1.2.3", Stdout: &out, Stderr: &errOut})
	return code, out.String(), errOut.String()
}

func TestVersion(t *testing.T) {
	code, out, _ := run(t, "version")
	if code != 0 || out != "rexd v1.2.3\n" {
		t.Fatalf("version: code %d, output %q", code, out)
	}
	if code, out, _ := run(t, "--version"); code != 0 || out != "rexd v1.2.3\n" {
		t.Fatalf("--version: code %d, output %q", code, out)
	}
}

func TestNoArgumentsPrintsUsage(t *testing.T) {
	code, _, errOut := run(t)
	if code != 2 || !strings.Contains(errOut, "Usage:") {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
}

func TestUnknownCommand(t *testing.T) {
	code, _, errOut := run(t, "frobnicate")
	if code != 2 || !strings.Contains(errOut, `unknown command "frobnicate"`) {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
}

func TestHelp(t *testing.T) {
	code, out, _ := run(t, "help")
	if code != 0 || !strings.Contains(out, "Commands:") {
		t.Fatalf("help: code %d, output %q", code, out)
	}
	code, out, _ = run(t, "help", "version")
	if code != 0 || !strings.Contains(out, "Usage: rexd version") {
		t.Fatalf("help version: code %d, output %q", code, out)
	}
	if code, _, _ := run(t, "help", "nope"); code != 2 {
		t.Fatalf("help nope: code %d, want 2", code)
	}
}

func TestVersionRejectsArguments(t *testing.T) {
	code, _, errOut := run(t, "version", "extra")
	if code != 2 || !strings.Contains(errOut, "takes no arguments") {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
}
