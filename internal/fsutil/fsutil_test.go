package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "file.json")
	if err := WriteFileAtomic(path, []byte("one"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("two"), 0o640); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "two" {
		t.Fatalf("content = %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("left temporary files behind: %v", entries)
	}
}

func TestCopyDir(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "top.txt"), "top", 0o644)
	mustWrite(t, filepath.Join(src, "bin", "tool"), "#!/bin/sh\n", 0o755)
	if err := os.Symlink("top.txt", filepath.Join(src, "link.txt")); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "copy")
	if err := CopyDir(src, dst); err != nil {
		t.Fatal(err)
	}

	if got, _ := os.ReadFile(filepath.Join(dst, "top.txt")); string(got) != "top" {
		t.Errorf("top.txt = %q", got)
	}
	info, err := os.Stat(filepath.Join(dst, "bin", "tool"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("tool mode = %v", info.Mode().Perm())
	}
	if target, err := os.Readlink(filepath.Join(dst, "link.txt")); err != nil || target != "top.txt" {
		t.Errorf("link = %q, %v", target, err)
	}
}

func TestCopyDirWhenTheFolderIsALink(t *testing.T) {
	source := t.TempDir()
	mustWrite(t, filepath.Join(source, "top.txt"), "top", 0o644)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "copy")
	if err := CopyDir(link, dst); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("the copy is a link, not a folder")
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "top.txt")); string(got) != "top" {
		t.Errorf("top.txt = %q", got)
	}
}

func TestCopyDirWhenTheFolderIsARelativeLink(t *testing.T) {
	parent := t.TempDir()
	mustWrite(t, filepath.Join(parent, "real", "top.txt"), "top", 0o644)
	if err := os.Symlink("real", filepath.Join(parent, "link")); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "copy")
	if err := CopyDir(filepath.Join(parent, "link"), dst); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "top.txt")); string(got) != "top" {
		t.Errorf("top.txt = %q", got)
	}
}

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.lua")
	mustWrite(t, src, "x", 0o644)
	dst := filepath.Join(dir, "deep", "er", "b.lua")
	if err := CopyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "x" {
		t.Fatalf("content = %q", got)
	}
}

func mustWrite(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}
