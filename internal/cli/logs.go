package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/tornikegomareli/rexd/internal/manifest"
	"github.com/tornikegomareli/rexd/internal/paths"
	"github.com/tornikegomareli/rexd/internal/state"
)

const followInterval = 250 * time.Millisecond

func runLogs(ctx context.Context, a *app, args []string) error {
	fs := a.flags("logs")
	script := fs.String("script", "", "only this script of the plugin, by `name`")
	daemonLog := fs.Bool("daemon", false, "the daemon's own log")
	lines := fs.Int("n", 50, "how many `lines` to print")
	follow := fs.Bool("f", false, "keep printing new lines")
	if err := a.parse(fs, args); err != nil {
		return err
	}
	if *lines < 0 {
		return usagef("-n must not be negative")
	}

	dirs, err := paths.Default()
	if err != nil {
		return err
	}
	files, err := logFiles(dirs, fs.Args(), *script, *daemonLog)
	if err != nil {
		return err
	}
	if *follow && len(files) > 1 {
		return usagef("-f follows one script; choose it with --script")
	}
	for i, file := range files {
		if len(files) > 1 {
			if i > 0 {
				fmt.Fprintln(a.stdout)
			}
			fmt.Fprintf(a.stdout, "==> %s <==\n", file)
		}
		if err := printTail(a.stdout, file, *lines); err != nil {
			return err
		}
	}
	if *follow {
		return followFile(ctx, a.stdout, files[0])
	}
	return nil
}

// logFiles works out which log files the arguments mean.
func logFiles(dirs paths.Dirs, args []string, script string, daemonLog bool) ([]string, error) {
	if daemonLog {
		if len(args) > 0 || script != "" {
			return nil, usagef("--daemon takes no plugin or script")
		}
		return []string{dirs.DaemonLog()}, nil
	}
	if len(args) != 1 {
		return nil, usagef("needs one plugin, or --daemon")
	}
	id := args[0]
	current, err := state.Load(dirs.StateFile())
	if err != nil {
		return nil, err
	}
	if _, ok := current.Plugins[id]; !ok {
		return nil, fmt.Errorf("%s is not installed", id)
	}
	man, _, err := manifest.Load(dirs.Plugin(id))
	if err != nil {
		return nil, err
	}
	if len(man.Scripts) == 0 {
		return nil, fmt.Errorf("%s has no event scripts, so it has no logs", id)
	}
	var files []string
	for _, s := range man.Scripts {
		if script == "" || s.Name == script {
			files = append(files, dirs.ScriptLog(id, s.Name))
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s has no script named %q", id, script)
	}
	return files, nil
}

// printTail prints the last lines of a log. When the current file holds too few,
// the rotated file supplies the rest. A log that does not exist yet has no lines.
func printTail(w io.Writer, path string, want int) error {
	current, err := readLines(path)
	if err != nil {
		return err
	}
	if len(current) < want {
		older, err := readLines(path + ".1")
		if err != nil {
			return err
		}
		current = append(older, current...)
	}
	if len(current) > want {
		current = current[len(current)-want:]
	}
	for _, line := range current {
		if _, err := w.Write(line); err != nil {
			return err
		}
	}
	return nil
}

func readLines(path string) ([][]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lines := bytes.SplitAfter(data, []byte("\n"))
	if last := len(lines) - 1; len(lines[last]) == 0 {
		lines = lines[:last] // A file that ends in a newline has no line after it.
	}
	return lines, nil
}

// followFile prints what is added to a log until ctx ends. When the log rotates,
// it starts over at the top of the new file.
func followFile(ctx context.Context, w io.Writer, path string) error {
	offset := int64(0)
	if info, err := os.Stat(path); err == nil {
		offset = info.Size()
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(followInterval):
		}
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			offset = 0
			continue
		}
		if err != nil {
			return err
		}
		if info.Size() < offset {
			offset = 0 // Rotated.
		}
		if info.Size() == offset {
			continue
		}
		read, err := copyFrom(w, path, offset)
		if err != nil {
			return err
		}
		offset += read
	}
}

func copyFrom(w io.Writer, path string, offset int64) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	return io.Copy(w, bufio.NewReader(file))
}
