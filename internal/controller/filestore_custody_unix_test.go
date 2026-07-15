//go:build !windows

package controller

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestFileStoreRejectsUnsafeConfiguredRoot(t *testing.T) {
	t.Run("non-directory", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state")
		if err := os.WriteFile(path, []byte("not a directory"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewFileStore(path); err == nil || !strings.Contains(err.Error(), "real directory") {
			t.Fatalf("NewFileStore(non-directory) err = %v, want real-directory rejection", err)
		}
	})

	t.Run("group-writable", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state")
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0770); err != nil {
			t.Fatal(err)
		}
		if _, err := NewFileStore(path); err == nil || !strings.Contains(err.Error(), "group/world-writable") {
			t.Fatalf("NewFileStore(group-writable root) err = %v, want custody rejection", err)
		}
	})

	t.Run("wrong-owner", func(t *testing.T) {
		if os.Geteuid() != 0 {
			// The filesystem root is conventionally owned by uid 0 and is only
			// inspected here; NewFileStore returns before creating any child.
			info, statErr := os.Lstat(string(os.PathSeparator))
			if statErr != nil {
				t.Skipf("cannot inspect wrong-owner fixture: %v", statErr)
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || stat.Uid == uint32(os.Geteuid()) {
				t.Skip("no safe wrong-owner fixture is available")
			}
			if _, err := NewFileStore(string(os.PathSeparator)); err == nil || !strings.Contains(err.Error(), "owned by uid") {
				t.Fatalf("NewFileStore(wrong-owner root) err = %v, want owner rejection", err)
			}
			return
		}
		path := filepath.Join(t.TempDir(), "state")
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(path, 65534, -1); err != nil {
			t.Skipf("cannot create wrong-owner fixture: %v", err)
		}
		if _, err := NewFileStore(path); err == nil || !strings.Contains(err.Error(), "owned by uid") {
			t.Fatalf("NewFileStore(wrong-owner root) err = %v, want owner rejection", err)
		}
	})
}

func TestFileStoreRejectsSymlinkCustodyDirectories(t *testing.T) {
	t.Run("configured-root", func(t *testing.T) {
		base := t.TempDir()
		target := filepath.Join(base, "target")
		if err := os.Mkdir(target, 0700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(base, "state")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := NewFileStore(link); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("NewFileStore(symlink root) err = %v, want symlink rejection", err)
		}
	})

	t.Run("telemetry-subdirectory", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "state")
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "telemetry-target")
		if err := os.Mkdir(target, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, "telemetry-history")); err != nil {
			t.Fatal(err)
		}
		if _, err := NewFileStore(root); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("NewFileStore(symlink telemetry root) err = %v, want symlink rejection", err)
		}
	})

	t.Run("tenant", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "state")
		fs, err := NewFileStore(root)
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "tenant-target")
		if err := os.Mkdir(target, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, "acme")); err != nil {
			t.Fatal(err)
		}
		if _, err := fs.ensureTenantDir("acme"); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("ensureTenantDir(symlink tenant) err = %v, want symlink rejection", err)
		}
	})

	t.Run("collection-subdirectory", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "state")
		fs, err := NewFileStore(root)
		if err != nil {
			t.Fatal(err)
		}
		tenantDir, err := fs.ensureTenantDir("acme")
		if err != nil {
			t.Fatal(err)
		}
		nodesDir := filepath.Join(tenantDir, "nodes")
		if err := os.Remove(nodesDir); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "nodes-target")
		if err := os.Mkdir(target, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, nodesDir); err != nil {
			t.Fatal(err)
		}
		if _, err := fs.ensureTenantDir("acme"); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("ensureTenantDir(symlink subdir) err = %v, want symlink rejection", err)
		}
	})
}

func TestFileStoreRejectsUnsafeTenantCustodyDirectories(t *testing.T) {
	t.Run("tenant-is-not-directory", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "state")
		fs, err := NewFileStore(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "acme"), []byte("not a directory"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := fs.ensureTenantDir("acme"); err == nil || !strings.Contains(err.Error(), "real directory") {
			t.Fatalf("ensureTenantDir(non-directory tenant) err = %v, want rejection", err)
		}
	})

	t.Run("tenant-is-group-writable", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "state")
		fs, err := NewFileStore(root)
		if err != nil {
			t.Fatal(err)
		}
		tenantDir := filepath.Join(root, "acme")
		if err := os.Mkdir(tenantDir, 0770); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(tenantDir, 0770); err != nil {
			t.Fatal(err)
		}
		if _, err := fs.ensureTenantDir("acme"); err == nil || !strings.Contains(err.Error(), "group/world-writable") {
			t.Fatalf("ensureTenantDir(group-writable tenant) err = %v, want rejection", err)
		}
	})

	t.Run("collection-is-group-writable", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "state")
		fs, err := NewFileStore(root)
		if err != nil {
			t.Fatal(err)
		}
		tenantDir, err := fs.ensureTenantDir("acme")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(tenantDir, "nodes"), 0770); err != nil {
			t.Fatal(err)
		}
		if _, err := fs.ensureTenantDir("acme"); err == nil || !strings.Contains(err.Error(), "group/world-writable") {
			t.Fatalf("ensureTenantDir(group-writable subdir) err = %v, want rejection", err)
		}
	})
}

func TestFileStoreRevalidatesRootAtOperation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	fs, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	moved := root + "-moved"
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, root); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ensureTenantDir("acme"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("operation after root replacement err = %v, want symlink rejection", err)
	}
}

func TestWriteBytesDurableDoesNotFollowPreplantedTempSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	const sentinel = "must remain unchanged"
	if err := os.WriteFile(victim, []byte(sentinel), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "record.json")
	legacyTemp := path + ".tmp"
	if err := os.Symlink(victim, legacyTemp); err != nil {
		t.Fatal(err)
	}

	want := []byte(`{"safe":true}`)
	if err := writeBytesDurable(path, want); err != nil {
		t.Fatalf("writeBytesDurable: %v", err)
	}
	if got, err := os.ReadFile(victim); err != nil || string(got) != sentinel {
		t.Fatalf("preplanted temp target = %q, %v; want unchanged %q", got, err, sentinel)
	}
	if info, err := os.Lstat(legacyTemp); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("preplanted temp symlink was altered: info=%v err=%v", info, err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(want) {
		t.Fatalf("installed record = %q, %v; want %q", got, err, want)
	}
	leaked, err := filepath.Glob(filepath.Join(dir, ".record.json.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leaked) != 0 {
		t.Fatalf("random temporary files leaked after install: %v", leaked)
	}
}
