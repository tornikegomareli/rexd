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

func TestSymlinkAtomicReplacesAFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "link")
	if err := WriteFileAtomic(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SymlinkAtomic("/some/target", path); err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(path); err != nil || got != "/some/target" {
		t.Fatalf("link = %q, %v", got, err)
	}
}

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.lua")
	mustWrite(t, src, "x", 0o644)
	dst := filepath.Join(dir, "deep", "er", "b.lua")
	if err := CopyFileAtomic(src, dst); err != nil {
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

func TestHashDirSeesContentNamesAndModes(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string, mode os.FileMode) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	hash := func() string {
		t.Helper()
		got, err := HashDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	write("a/b.txt", "one", 0o644)
	first := hash()
	if hash() != first {
		t.Fatal("the same folder has two hashes")
	}
	write("a/b.txt", "two", 0o644)
	second := hash()
	if second == first {
		t.Error("new content has the same hash")
	}
	write("a/b.txt", "two", 0o755)
	if hash() == second {
		t.Error("a new mode has the same hash")
	}
	write("a/b.txt", "two", 0o644)
	if err := os.Rename(filepath.Join(dir, "a/b.txt"), filepath.Join(dir, "a/c.txt")); err != nil {
		t.Fatal(err)
	}
	if hash() == second {
		t.Error("a new name has the same hash")
	}
	before := hash()
	write(".git/HEAD", "ref", 0o644)
	if hash() != before {
		t.Error(".git changed the hash")
	}
}
