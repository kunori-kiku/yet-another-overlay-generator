//go:build !windows

package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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

	for _, mode := range []os.FileMode{0775, 0777} {
		mode := mode
		t.Run(fmt.Sprintf("owned writable root %04o is tightened", mode), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state")
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			if _, err := NewFileStore(path); err != nil {
				t.Fatalf("NewFileStore(owned %04o root): %v", mode, err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0700 {
				t.Fatalf("owned root mode after open = %04o, want 0700", got)
			}
		})
	}

	t.Run("sticky shared root is rejected unchanged", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "shared")
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, os.ModeSticky|0777); err != nil {
			t.Fatal(err)
		}
		if _, err := NewFileStore(path); err == nil || !strings.Contains(err.Error(), "group/world-writable") {
			t.Fatalf("NewFileStore(sticky shared root) err = %v, want custody rejection", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode(); got.Perm() != 0777 || got&os.ModeSticky == 0 {
			t.Fatalf("sticky shared root mode after rejection = %v, want unchanged 01777", got)
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

func newStableRecordCustodyFixture(t *testing.T) (*FileStore, TenantID, string) {
	t.Helper()
	fs, err := NewFileStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	tenant := TenantID("acme")
	dir, err := fs.ensureTenantDir(tenant)
	if err != nil {
		t.Fatalf("ensureTenantDir: %v", err)
	}
	return fs, tenant, dir
}

func requireStableRecordCustodyError(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "symlink or special file") {
		t.Fatalf("stable-record operation err = %v, want symlink/special-file custody rejection", err)
	}
}

func TestFileStoreRejectsSymlinkStableRecords(t *testing.T) {
	t.Run("keyed get list exists and delete", func(t *testing.T) {
		fs, tenant, dir := newStableRecordCustodyFixture(t)
		victim := filepath.Join(t.TempDir(), "outside.json")
		const sentinel = `{"outside":true}`
		if err := os.WriteFile(victim, []byte(sentinel), 0600); err != nil {
			t.Fatal(err)
		}
		record := filepath.Join(dir, "nodes", "node-1.json")
		if err := os.Symlink(victim, record); err != nil {
			t.Fatal(err)
		}

		if got, err := fs.get(tenant, collNodes, "node-1"); err == nil {
			t.Fatalf("get followed stable symlink and returned %q", got)
		} else {
			requireStableRecordCustodyError(t, err)
		}
		if got, err := fs.list(tenant, collNodes); err == nil {
			t.Fatalf("list accepted stable symlink and returned %v", got)
		} else {
			requireStableRecordCustodyError(t, err)
		}
		if exists, err := fs.exists(tenant, collNodes, "node-1"); err == nil {
			t.Fatalf("exists(symlink) = %v, nil; want hard custody error", exists)
		} else {
			requireStableRecordCustodyError(t, err)
		}
		if err := fs.del(tenant, collNodes, "node-1"); err == nil {
			t.Fatal("del(symlink) = nil, want hard custody error")
		} else {
			requireStableRecordCustodyError(t, err)
		}
		if info, err := os.Lstat(record); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("delete altered planted symlink: info=%v err=%v", info, err)
		}
		if got, err := os.ReadFile(victim); err != nil || string(got) != sentinel {
			t.Fatalf("outside target = %q, %v; want unchanged %q", got, err, sentinel)
		}
	})

	t.Run("singleton", func(t *testing.T) {
		fs, tenant, dir := newStableRecordCustodyFixture(t)
		victim := filepath.Join(t.TempDir(), "outside-settings.json")
		if err := os.WriteFile(victim, []byte(`{"telemetry_history_limit":1}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, filepath.Join(dir, "settings.json")); err != nil {
			t.Fatal(err)
		}
		if got, err := fs.get(tenant, collSettings, ""); err == nil {
			t.Fatalf("singleton get followed stable symlink and returned %q", got)
		} else {
			requireStableRecordCustodyError(t, err)
		}
	})

	t.Run("generation", func(t *testing.T) {
		_, _, dir := newStableRecordCustodyFixture(t)
		victim := filepath.Join(t.TempDir(), "outside-generation.json")
		if err := os.WriteFile(victim, []byte(`{"generation":42}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, filepath.Join(dir, generationFileName)); err != nil {
			t.Fatal(err)
		}
		if got, err := readGeneration(dir); err == nil {
			t.Fatalf("readGeneration followed stable symlink and returned %d", got)
		} else {
			requireStableRecordCustodyError(t, err)
		}
	})
}

func TestFileStoreRejectsSymlinkAuditFiles(t *testing.T) {
	t.Run("jsonl read and first append", func(t *testing.T) {
		fs, tenant, dir := newStableRecordCustodyFixture(t)
		victim := filepath.Join(t.TempDir(), "outside-audit.jsonl")
		const sentinel = "outside audit must remain unchanged\n"
		if err := os.WriteFile(victim, []byte(sentinel), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, filepath.Join(dir, auditFileName)); err != nil {
			t.Fatal(err)
		}
		if got, err := fs.listAudit(tenant); err == nil {
			t.Fatalf("listAudit followed stable symlink and returned %v", got)
		} else {
			requireStableRecordCustodyError(t, err)
		}
		if got, err := fs.appendAudit(tenant, AuditEntry{Actor: "operator", Action: "unsafe"}); err == nil {
			t.Fatalf("appendAudit followed stable symlink and returned %+v", got)
		} else {
			requireStableRecordCustodyError(t, err)
		}
		if got, err := os.ReadFile(victim); err != nil || string(got) != sentinel {
			t.Fatalf("outside audit target = %q, %v; want unchanged %q", got, err, sentinel)
		}
	})

	t.Run("legacy read", func(t *testing.T) {
		fs, tenant, dir := newStableRecordCustodyFixture(t)
		victim := filepath.Join(t.TempDir(), "outside-audit.json")
		if err := os.WriteFile(victim, []byte(`[]`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, filepath.Join(dir, legacyAuditFileName)); err != nil {
			t.Fatal(err)
		}
		if got, err := fs.listAudit(tenant); err == nil {
			t.Fatalf("listAudit followed legacy stable symlink and returned %v", got)
		} else {
			requireStableRecordCustodyError(t, err)
		}
	})

	t.Run("cached tail cannot bypass append validation", func(t *testing.T) {
		fs, tenant, dir := newStableRecordCustodyFixture(t)
		if _, err := fs.appendAudit(tenant, AuditEntry{Actor: "operator", Action: "seed"}); err != nil {
			t.Fatalf("seed appendAudit: %v", err)
		}
		auditPath := filepath.Join(dir, auditFileName)
		if err := os.Rename(auditPath, auditPath+".saved"); err != nil {
			t.Fatal(err)
		}
		victim := filepath.Join(t.TempDir(), "outside-cached-tail.jsonl")
		const sentinel = "cached tail must not redirect append\n"
		if err := os.WriteFile(victim, []byte(sentinel), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, auditPath); err != nil {
			t.Fatal(err)
		}
		if got, err := fs.appendAudit(tenant, AuditEntry{Actor: "operator", Action: "unsafe"}); err == nil {
			t.Fatalf("cached-tail append followed stable symlink and returned %+v", got)
		} else {
			requireStableRecordCustodyError(t, err)
		}
		if got, err := os.ReadFile(victim); err != nil || string(got) != sentinel {
			t.Fatalf("outside cached-tail target = %q, %v; want unchanged %q", got, err, sentinel)
		}
	})
}

func TestFileStoreRejectsSpecialStableRecordsWithoutBlocking(t *testing.T) {
	tests := []struct {
		name string
		run  func(*FileStore, TenantID) error
	}{
		{name: "get", run: func(fs *FileStore, tenant TenantID) error {
			_, err := fs.get(tenant, collNodes, "node-1")
			return err
		}},
		{name: "list", run: func(fs *FileStore, tenant TenantID) error {
			_, err := fs.list(tenant, collNodes)
			return err
		}},
		{name: "exists", run: func(fs *FileStore, tenant TenantID) error {
			_, err := fs.exists(tenant, collNodes, "node-1")
			return err
		}},
		{name: "delete", run: func(fs *FileStore, tenant TenantID) error {
			return fs.del(tenant, collNodes, "node-1")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs, tenant, dir := newStableRecordCustodyFixture(t)
			if err := syscall.Mkfifo(filepath.Join(dir, "nodes", "node-1.json"), 0600); err != nil {
				t.Fatalf("mkfifo: %v", err)
			}
			result := make(chan error, 1)
			go func() { result <- tc.run(fs, tenant) }()
			select {
			case err := <-result:
				requireStableRecordCustodyError(t, err)
			case <-time.After(2 * time.Second):
				t.Fatal("stable-record operation blocked on FIFO")
			}
		})
	}

	t.Run("audit append", func(t *testing.T) {
		fs, tenant, dir := newStableRecordCustodyFixture(t)
		if err := syscall.Mkfifo(filepath.Join(dir, auditFileName), 0600); err != nil {
			t.Fatalf("mkfifo: %v", err)
		}
		result := make(chan error, 1)
		go func() {
			_, err := fs.appendAudit(tenant, AuditEntry{Actor: "operator", Action: "unsafe"})
			result <- err
		}()
		select {
		case err := <-result:
			requireStableRecordCustodyError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatal("audit append blocked on FIFO")
		}
	})
}

func TestStoreFileOpenFlagsKeepFIFORaceNonblocking(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "record.json"), 0600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer root.Close()

	result := make(chan error, 1)
	go func() {
		f, err := root.OpenFile("record.json", storeFileOpenFlags(os.O_RDONLY), 0)
		if err == nil {
			err = f.Close()
		}
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("nonblocking FIFO open: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("store file open flags blocked on FIFO")
	}
}

func TestFileStoreStableRecordCompatibilitySemantics(t *testing.T) {
	fs, tenant, dir := newStableRecordCustodyFixture(t)

	if _, err := fs.get(tenant, collNodes, "missing"); err != errKVNotFound {
		t.Fatalf("get(missing) err = %v, want errKVNotFound", err)
	}
	if got, err := fs.list(tenant, collNodes); err != nil || len(got) != 0 {
		t.Fatalf("list(empty) = %v, %v; want empty, nil", got, err)
	}
	if exists, err := fs.exists(tenant, collNodes, "missing"); err != nil || exists {
		t.Fatalf("exists(missing) = %v, %v; want false, nil", exists, err)
	}
	if err := fs.del(tenant, collNodes, "missing"); err != nil {
		t.Fatalf("del(missing): %v", err)
	}
	if got, err := readGeneration(dir); err != nil || got != 0 {
		t.Fatalf("readGeneration(absent) = %d, %v; want 0, nil", got, err)
	}

	corrupt := filepath.Join(dir, "nodes", "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if exists, err := fs.exists(tenant, collNodes, "corrupt"); err != nil || !exists {
		t.Fatalf("exists(corrupt regular) = %v, %v; want true, nil", exists, err)
	}

	// Controller writes remain 0600, but the no-follow fix must not turn an
	// older/restored same-owner regular record into a retroactive outage solely
	// because its mode is more permissive.
	permissive := filepath.Join(dir, "nodes", "permissive.json")
	const permissiveBody = `{"compatible":true}`
	if err := os.WriteFile(permissive, []byte(permissiveBody), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(permissive, 0644); err != nil {
		t.Fatal(err)
	}
	if got, err := fs.get(tenant, collNodes, "permissive"); err != nil || string(got) != permissiveBody {
		t.Fatalf("get(permissive regular) = %q, %v; want compatible bytes", got, err)
	}

	if _, err := fs.AppendAudit(context.Background(), tenant, AuditEntry{Actor: "operator", Action: "created"}); err != nil {
		t.Fatalf("AppendAudit(create): %v", err)
	}
	auditInfo, err := os.Lstat(filepath.Join(dir, auditFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !auditInfo.Mode().IsRegular() || auditInfo.Mode().Perm() != 0600 {
		t.Fatalf("created audit mode = %v, want regular 0600", auditInfo.Mode())
	}
}
