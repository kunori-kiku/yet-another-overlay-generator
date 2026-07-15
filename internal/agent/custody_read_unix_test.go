//go:build !windows

package agent

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCustodyReadPermissionClasses(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "controller.token")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadPrivateFile(secret); err != nil || string(got) != "secret" {
		t.Fatalf("secure secret read = %q, %v", got, err)
	}
	if err := os.Chmod(secret, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivateFile(secret); err == nil || !strings.Contains(err.Error(), "group/world-accessible") {
		t.Fatalf("group-readable secret error = %v, want custody refusal", err)
	}

	pin := filepath.Join(dir, "operator-cred.pem")
	if err := os.WriteFile(pin, []byte("public pin"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadProtectedFile(pin); err != nil || string(got) != "public pin" {
		t.Fatalf("read-only public pin read = %q, %v", got, err)
	}
	if err := os.Chmod(pin, 0o664); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadProtectedFile(pin); err == nil || !strings.Contains(err.Error(), "group/world-writable") {
		t.Fatalf("group-writable public pin error = %v, want custody refusal", err)
	}
}

func TestCustodyReadRejectsUnsafeDirectParent(t *testing.T) {
	root := t.TempDir()

	t.Run("writable directory", func(t *testing.T) {
		dir := filepath.Join(root, "writable")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "state.json")
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadProtectedFile(path); err == nil || !strings.Contains(err.Error(), "group/world-writable") {
			t.Fatalf("unsafe parent error = %v, want custody refusal", err)
		}
	})

	t.Run("symlink directory", func(t *testing.T) {
		realDir := filepath.Join(root, "real")
		linkDir := filepath.Join(root, "linked")
		if err := os.Mkdir(realDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(realDir, "state.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(realDir, linkDir); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadProtectedFile(filepath.Join(linkDir, "state.json")); err == nil {
			t.Fatal("read accepted a symlink direct custody directory")
		}
	})

	t.Run("wrong-owner directory", func(t *testing.T) {
		if os.Geteuid() != 0 {
			t.Skip("changing a fixture owner requires root")
		}
		dir := filepath.Join(root, "foreign")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "state.json")
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(dir, 65534, 65534); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadProtectedFile(path); err == nil || !strings.Contains(err.Error(), "owned by uid") {
			t.Fatalf("wrong-owner parent error = %v, want ownership refusal", err)
		}
	})
}

func TestCustodyReadRejectsLinkAndSpecialFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("do not follow"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "pin.pem")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadProtectedFile(link); err == nil {
		t.Fatal("read accepted a symlink custody file")
	}

	fifo := filepath.Join(dir, "state.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadProtectedFile(fifo); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("special-file error = %v, want regular-file refusal", err)
	}
}

func TestCustodyReadRejectsWrongOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing a fixture owner requires root")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadProtectedFile(path); err == nil || !strings.Contains(err.Error(), "owned by uid") {
		t.Fatalf("wrong-owner error = %v, want ownership refusal", err)
	}
}

func TestLoadStateRejectsWritableIntegrityFile(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.WriteFile(statePath(stateDir), []byte(`{"node_id":"attacker"}`), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(statePath(stateDir), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(stateDir); err == nil || !strings.Contains(err.Error(), "group/world-writable") {
		t.Fatalf("LoadState error = %v, want custody refusal before parsing", err)
	}
}

func TestCustodyReadIsBounded(t *testing.T) {
	_, err := readCustodyBytes(bytes.NewReader(make([]byte, maxCustodyFileBytes+1)), "oversized")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized custody read error = %v, want bounded refusal", err)
	}
}

func TestEarlySelfUpdateFailsClosedOnUnsafeStateCustody(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.WriteFile(statePath(stateDir), []byte(`{"pending_update":{"from":"1.0.0","to":"1.1.0"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(statePath(stateDir), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileSelfUpdateEarly(stateDir, "1.0.0", io.Discard); err == nil || !strings.Contains(err.Error(), "group/world-writable") {
		t.Fatalf("early reconcile error = %v, want unsafe-state refusal", err)
	}
	if err := os.Chmod(statePath(stateDir), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileSelfUpdateEarly(stateDir, "1.0.0", io.Discard); err != nil {
		t.Fatalf("reconcile after operator repaired state custody: %v", err)
	}
	st, err := LoadState(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if st.PendingUpdate == nil || st.PendingUpdate.Attempts != 1 {
		t.Fatalf("repaired reconcile did not persist one attempt: %+v", st.PendingUpdate)
	}
}
