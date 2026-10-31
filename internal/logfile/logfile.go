// Package logfile writes log files that cannot grow without limit and prefixes
// each line of a program's output with a timestamp.
package logfile

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultMaxSize is the size at which a log file is rotated.
const DefaultMaxSize = 1 << 20 // 1 MiB

// maxPartialLine limits how much of an unfinished line a LineWriter holds.
const maxPartialLine = 64 << 10

// Writer appends to a file. When the file would exceed its maximum size, the
// file becomes path.1, replacing any older one, and writing starts again in a
// new file. So at most two files exist, and at most twice the maximum size.
type Writer struct {
	path    string
	maxSize int64

	mu   sync.Mutex
	file *os.File
	size int64
}

// Open opens the log at path, creating its folder if needed.
func Open(path string, maxSize int64) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	w := &Writer{path: path, maxSize: maxSize}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Writer) open() error {
	file, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	w.file, w.size = file, info.Size()
	return nil
}

// Write appends p, rotating first if needed.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, os.ErrClosed
	}
	if w.size > 0 && w.size+int64(len(p)) > w.maxSize {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *Writer) rotate() error {
	if err := w.file.Close(); err != nil {
		return err
	}
	w.file = nil
	if err := os.Rename(w.path, w.path+".1"); err != nil {
		return err
	}
	return w.open()
}

// Close closes the file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// LineWriter prefixes each complete line with a timestamp before passing it on.
// It holds an unfinished line back until its end arrives or Flush is called.
type LineWriter struct {
	out     io.Writer
	now     func() time.Time
	partial []byte
}

// NewLineWriter returns a LineWriter that writes to out.
func NewLineWriter(out io.Writer, now func() time.Time) *LineWriter {
	return &LineWriter{out: out, now: now}
}

// Write takes output in any chunks. It always reports all of p as written, so a
// full disk cannot block the program whose output this is.
func (l *LineWriter) Write(p []byte) (int, error) {
	n := len(p)
	for {
		end := bytes.IndexByte(p, '\n')
		if end < 0 {
			break
		}
		l.emit(append(l.partial, p[:end+1]...))
		l.partial = l.partial[:0]
		p = p[end+1:]
	}
	l.partial = append(l.partial, p...)
	if len(l.partial) > maxPartialLine {
		l.Flush()
	}
	return n, nil
}

// Flush writes an unfinished line.
func (l *LineWriter) Flush() {
	if len(l.partial) == 0 {
		return
	}
	l.emit(append(l.partial, '\n'))
	l.partial = l.partial[:0]
}

func (l *LineWriter) emit(line []byte) {
	stamp := l.now().Format(time.RFC3339)
	out := make([]byte, 0, len(stamp)+1+len(line))
	out = append(append(append(out, stamp...), ' '), line...)
	// Only a write failure can happen here, and output has nowhere else to go.
	_, _ = l.out.Write(out)
}
