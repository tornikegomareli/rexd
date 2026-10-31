// Package fakerex provides a small program that stands in for the rex command in
// tests. It copies the behavior measured against a real Rex server: exit status 4
// when the server is gone, exit status 3 when no session can be picked, and exit
// status 1 for a config with errors.
//
// The program is built once per test binary and without the race detector. A
// race-enabled binary takes about a second to start on macOS, and tests run the
// fake often. Use RunTests in TestMain and New in each test.
package fakerex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// EnvDir names the variable that holds a fake's control folder.
const EnvDir = "REXD_FAKEREX_DIR"

const (
	fileDown       = "down"
	fileSessions   = "sessions"
	fileAPI        = "api"
	fileConfigPath = "config-path"
	fileCalls      = "calls.jsonl"
	fileStarts     = "starts.jsonl"

	exitConfigErrors = 1
	exitNoTarget     = 3
	exitUnavailable  = 4
)

var (
	buildOnce sync.Once
	binaryDir string
	binary    string
	buildErr  error
)

// RunTests runs a package's tests and removes the fake program afterwards.
func RunTests(m *testing.M) int {
	code := m.Run()
	if binaryDir != "" {
		os.RemoveAll(binaryDir)
	}
	return code
}

func build() (string, error) {
	buildOnce.Do(func() {
		binaryDir, buildErr = os.MkdirTemp("", "fakerex")
		if buildErr != nil {
			return
		}
		binary = filepath.Join(binaryDir, "rex")
		cmd := exec.Command("go", "build", "-o", binary, "github.com/tornikegomareli/rexd/internal/testutil/fakerex/cmd/fakerex")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("building the fake rex: %w\n%s", err, out)
		}
	})
	return binary, buildErr
}

// Fake is a controllable rex command for one test.
type Fake struct {
	// Path is the program to run as rex.
	Path string
	// Env holds the variables that make Path behave as the fake.
	Env []string

	dir string
}

// Start records one start of an event script.
type Start struct {
	Script  string `json:"script"`
	Session string `json:"session"`
	PID     int    `json:"pid"`
	Dir     string `json:"dir"`
	Plugin  string `json:"plugin"`
	Block   string `json:"block"`
}

// New returns a fake with the server up and one session.
func New(t testing.TB) *Fake {
	t.Helper()
	path, err := build()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	f := &Fake{Path: path, Env: []string{EnvDir + "=" + dir}, dir: dir}
	f.SetSessions("session:one")
	return f
}

// SetDown makes the server unreachable, or reachable again.
func (f *Fake) SetDown(down bool) {
	path := filepath.Join(f.dir, fileDown)
	if down {
		f.write(fileDown, "")
		return
	}
	os.Remove(path)
}

// SetSessions sets the IDs that rex ls reports.
func (f *Fake) SetSessions(ids ...string) { f.write(fileSessions, strings.Join(ids, "\n")) }

// SetAPI sets the API revision that rex version reports.
func (f *Fake) SetAPI(revision int) { f.write(fileAPI, strconv.Itoa(revision)) }

// SetConfigPath sets the config path that rex keymap reports.
func (f *Fake) SetConfigPath(path string) { f.write(fileConfigPath, path) }

func (f *Fake) write(name, content string) {
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0o644); err != nil {
		panic(err)
	}
}

// Starts returns every recorded start of an event script.
func (f *Fake) Starts() []Start {
	return readJSONLines[Start](filepath.Join(f.dir, fileStarts))
}

// Calls returns the arguments of every call, global flags included.
func (f *Fake) Calls() [][]string {
	return readJSONLines[[]string](filepath.Join(f.dir, fileCalls))
}

// Reloads counts the rex config reload calls that reached a running server.
func (f *Fake) Reloads() int {
	n := 0
	for _, call := range f.Calls() {
		if strings.Contains(strings.Join(call, " "), "config reload") {
			n++
		}
	}
	return n
}

func readJSONLines[T any](path string) []T {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	var out []T
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var v T
		if err := json.Unmarshal(scanner.Bytes(), &v); err != nil {
			continue // A concurrent writer's partial line.
		}
		out = append(out, v)
	}
	return out
}

var appendMu sync.Mutex

func appendJSONLine(path string, v any) {
	appendMu.Lock()
	defer appendMu.Unlock()
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		panic(err)
	}
	defer file.Close()
	file.Write(append(data, '\n'))
}

// Run is the fake program's main function. It returns the exit status.
func Run(dir string, args []string) int {
	appendJSONLine(filepath.Join(dir, fileCalls), args)
	args = skipGlobalFlags(args)
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "fakerex: no command")
		return 2
	}
	down := exists(filepath.Join(dir, fileDown))
	unavailable := func() int {
		fmt.Fprintln(os.Stderr, "Error: no Rex server is running, and autostart is disabled")
		return exitUnavailable
	}

	switch args[0] {
	case "version":
		api := 7
		if data, err := os.ReadFile(filepath.Join(dir, fileAPI)); err == nil {
			api, _ = strconv.Atoi(string(data))
		}
		fmt.Printf(`{"rex_version":"fake","api_revision":%d}`+"\n", api)
		return 0
	case "ls":
		if down {
			return unavailable()
		}
		for _, id := range sessions(dir) {
			fmt.Println(id)
		}
		return 0
	case "keymap":
		if down {
			return unavailable()
		}
		data, _ := os.ReadFile(filepath.Join(dir, fileConfigPath))
		out, _ := json.Marshal(map[string]any{"config": map[string]string{"path": string(data)}})
		fmt.Println(string(out))
		return 0
	case "config":
		if len(args) < 2 {
			return 2
		}
		switch args[1] {
		case "check":
			return configCheck(args[2])
		case "reload":
			if down {
				return unavailable()
			}
			fmt.Println("generation: 2")
			return 0
		}
	case "do":
		if down {
			return unavailable()
		}
		return runScript(dir, args[1])
	}
	fmt.Fprintf(os.Stderr, "fakerex: unknown command %q\n", args)
	return 2
}

func skipGlobalFlags(args []string) []string {
	for len(args) > 0 {
		switch {
		case args[0] == "-S":
			args = args[2:]
		case strings.HasPrefix(args[0], "--autostart"):
			args = args[1:]
		default:
			return args
		}
	}
	return args
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func sessions(dir string) []string {
	data, _ := os.ReadFile(filepath.Join(dir, fileSessions))
	return strings.Fields(string(data))
}

// configCheck fails if the file, or a Lua file in the rexd folder beside it,
// contains the word BROKEN. The real rex would run the Lua.
func configCheck(path string) int {
	fmt.Printf("path: %s\nexists: yes\n", path)
	broken := func(file string) bool {
		data, err := os.ReadFile(file)
		return err == nil && strings.Contains(string(data), "BROKEN")
	}
	report := func(file string) int {
		fmt.Printf("error %s:1: broken\n", filepath.Base(file))
		fmt.Fprintln(os.Stderr, "Error: the configuration has errors")
		return exitConfigErrors
	}
	if broken(path) {
		return report(path)
	}
	root := filepath.Join(filepath.Dir(path), "rexd")
	found := ""
	filepath.WalkDir(root, func(file string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(file, ".lua") && broken(file) {
			found = file
		}
		return nil
	})
	if found != "" {
		return report(found)
	}
	return 0
}

// runScript acts out the directives in a script, one per line:
//
//	print TEXT     print a line
//	sleep DURATION wait
//	exit CODE      exit now
//	ignore-int     ignore SIGINT, so only SIGKILL stops the process
//
// Other lines are ignored. After the last line the process runs until it is
// stopped, or exits with status 4 when the server goes down.
func runScript(dir, script string) int {
	session := os.Getenv("REX_SESSION")
	if session == "" {
		if ids := sessions(dir); len(ids) != 1 {
			fmt.Fprintln(os.Stderr, "Error: block target default not found")
			return exitNoTarget
		}
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	wd, _ := os.Getwd()
	appendJSONLine(filepath.Join(dir, fileStarts), Start{
		Script:  script,
		Session: session,
		PID:     os.Getpid(),
		Dir:     wd,
		Plugin:  os.Getenv("REXD_PLUGIN_ID"),
		Block:   os.Getenv("REX_BLOCK"),
	})

	data, err := os.ReadFile(script)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}
	for line := range strings.Lines(string(data)) {
		word, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch word {
		case "print":
			fmt.Println(rest)
		case "sleep":
			d, _ := time.ParseDuration(rest)
			time.Sleep(d)
		case "exit":
			code, _ := strconv.Atoi(rest)
			return code
		case "ignore-int":
			signal.Reset(os.Interrupt)
			signal.Ignore(os.Interrupt)
		}
	}

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return 0
		case <-ticker.C:
			if exists(filepath.Join(dir, fileDown)) {
				fmt.Fprintln(os.Stderr, "Error: server went away")
				return exitUnavailable
			}
		}
	}
}
