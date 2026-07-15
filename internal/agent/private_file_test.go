package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWritePrivateFileAtomicReplacesAndTightensMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets", "token")
	if err := WritePrivateFileAtomic(path, []byte("first")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatalf("widen mode: %v", err)
	}
	if err := WritePrivateFileAtomic(path, []byte("replacement")); err != nil {
		t.Fatalf("replacement write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read replacement: %v", err)
	}
	if string(got) != "replacement" {
		t.Fatalf("content = %q, want replacement", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat replacement: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("mode = %04o, want 0600", got)
	}
}

func TestWritePrivateFileAtomicRejectsUnsafeParentAndSymlinkTarget(t *testing.T) {
	t.Run("unsafe parent", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "unsafe")
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.Chmod(dir, 0777); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		if err := WritePrivateFileAtomic(filepath.Join(dir, "token"), []byte("secret")); err == nil {
			t.Fatal("write accepted group/world-writable parent")
		}
	})

	t.Run("symlink target", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "outside")
		path := filepath.Join(dir, "token")
		if err := os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
			t.Fatalf("write target: %v", err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		if err := WritePrivateFileAtomic(path, []byte("secret")); err == nil {
			t.Fatal("write accepted symlink destination")
		}
		got, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("read target: %v", err)
		}
		if string(got) != "unchanged" {
			t.Fatalf("symlink target changed to %q", got)
		}
	})
}
