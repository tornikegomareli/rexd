package logfile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriterAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deep", "app.log")
	w, err := Open(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("one\n"))
	w.Close()

	w, err = Open(path, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.Write([]byte("two\n"))

	got, _ := os.ReadFile(path)
	if string(got) != "one\ntwo\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestWriterRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	w, err := Open(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.Write([]byte("12345678\n")) // 9 bytes, fits.
	w.Write([]byte("abcdef\n"))   // Would exceed 10, so the first write moves away.
	w.Write([]byte("xyz\n"))      // 11 bytes in total: rotates again and replaces .1.

	current, _ := os.ReadFile(path)
	if string(current) != "xyz\n" {
		t.Errorf("current = %q", current)
	}
	old, _ := os.ReadFile(path + ".1")
	if string(old) != "abcdef\n" {
		t.Errorf("old = %q", old)
	}
}

func TestWriterKeepsAnOversizedWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	w, err := Open(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	long := strings.Repeat("x", 100)
	if _, err := w.Write([]byte(long)); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != long {
		t.Fatalf("content = %q", got)
	}
	if _, err := os.Stat(path + ".1"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rotated an empty file")
	}
}

func TestWriteAfterCloseFails(t *testing.T) {
	w, err := Open(filepath.Join(t.TempDir(), "app.log"), 10)
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	if _, err := w.Write([]byte("x")); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("err = %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func fixedTime() time.Time { return time.Date(2026, 10, 31, 4, 0, 0, 0, time.UTC) }

const stamp = "2026-10-31T04:00:00Z "

func TestLineWriterPrefixesCompleteLines(t *testing.T) {
	var out bytes.Buffer
	l := NewLineWriter(&out, fixedTime)
	l.Write([]byte("hel"))
	if out.Len() != 0 {
		t.Fatalf("wrote a partial line: %q", out.String())
	}
	l.Write([]byte("lo\nsecond\nthi"))
	want := stamp + "hello\n" + stamp + "second\n"
	if out.String() != want {
		t.Fatalf("got %q", out.String())
	}
	l.Flush()
	want += stamp + "thi\n"
	if out.String() != want {
		t.Fatalf("after Flush: %q", out.String())
	}
	l.Flush()
	if out.String() != want {
		t.Fatalf("second Flush wrote again: %q", out.String())
	}
}

func TestLineWriterFlushesAVeryLongLine(t *testing.T) {
	var out bytes.Buffer
	l := NewLineWriter(&out, fixedTime)
	l.Write(bytes.Repeat([]byte("x"), maxPartialLine+1))
	if out.Len() == 0 {
		t.Fatal("held a very long line back")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestLineWriterNeverFails(t *testing.T) {
	l := NewLineWriter(failingWriter{}, fixedTime)
	n, err := l.Write([]byte("line\n"))
	if err != nil || n != 5 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
}
