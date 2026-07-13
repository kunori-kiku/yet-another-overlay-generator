package controller

// filestore_audit.go — FileStore's append-only, hash-chained audit-JSONL log: the
// read/write/rotate/migrate machinery and the O(1) AppendAudit tail cache. Split from
// filestore.go (plan-2); no logic change.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// auditTail is the in-memory tail of a tenant's audit log: the last entry's Seq and Hash
// (to chain the next append) and the live entry count (to trigger amortized rotation).
type auditTail struct {
	seq   int64
	hash  string
	count int
}

// --- audit ------------------------------------------------------------------

// auditFileName / legacyAuditFileName are the current (append-only JSONL) and the legacy
// (single JSON array) on-disk audit logs. A legacy file is migrated to JSONL on first
// access (loadAuditTail) so there is never a split-brain across the two formats.
const (
	auditFileName       = "audit.jsonl"
	legacyAuditFileName = "audit.json"
)

// readAudit returns the tenant's audit entries (empty slice when no log exists), in their
// stored Seq order. It reads the append-only JSONL log; if only a legacy audit.json array
// is present (pre-plan-6 data, not yet migrated by an append) it falls back to that, so
// ListAudit returns the full history either way.
func (fs *FileStore) readAudit(dir string) ([]AuditEntry, error) {
	entries, _, err := readAuditJSONL(filepath.Join(dir, auditFileName))
	if err != nil {
		return nil, err
	}
	if entries != nil {
		return entries, nil
	}
	// No JSONL yet — fall back to a (possibly present) legacy array.
	var legacy []AuditEntry
	if err := readJSON(filepath.Join(dir, legacyAuditFileName), &legacy); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return legacy, nil
}

// readAuditJSONL parses an append-only JSONL audit log (one AuditEntry per line). It
// returns (nil, false, nil) when the file does not exist so the caller can distinguish "no
// JSONL log" from "empty log". Blank lines are skipped (tolerant of a trailing newline).
//
// Crash tolerance: the append path is a bare O_APPEND write (not rename-atomic), so a crash
// or power loss can leave a partially-written FINAL line. That torn trailing line is DROPPED
// and reported via tornTail=true — preserving the durably-committed prefix so the log stays
// readable AND appendable (loadAuditTail self-heals by rewriting the clean prefix before the
// next append, so the torn bytes never become an interior line). A malformed INTERIOR line
// is real corruption, not a torn append, and is still surfaced as a hard error. This restores
// the store-wide "lose at most the last record, never corrupt/brick" guarantee that the
// rename-atomic writers provide.
func readAuditJSONL(path string) (entries []AuditEntry, tornTail bool, err error) {
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return nil, false, nil
		}
		return nil, false, rerr
	}
	lines := strings.Split(string(data), "\n")
	lastNonBlank := -1
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			lastNonBlank = i
		}
	}
	entries = []AuditEntry{}
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e AuditEntry
		if jerr := json.Unmarshal([]byte(line), &e); jerr != nil {
			if i == lastNonBlank {
				// A malformed FINAL line: drop it, keep the durable prefix. This is the torn
				// residue of a crashed append (the common case — a torn O_APPEND never has a
				// trailing newline), and ALSO subsumes on-disk corruption of just the last
				// record, which is indistinguishable and was previously a brick. So a clean
				// read is not proof the last record was uncorrupted — only INTERIOR corruption
				// (below) is surfaced.
				return entries, true, nil
			}
			return nil, false, fmt.Errorf("controller: parse %s: %w", auditFileName, jerr)
		}
		entries = append(entries, e)
	}
	return entries, false, nil
}

// writeAuditJSONL atomically AND durably rewrites the whole JSONL log via the shared
// writeBytesDurable primitive (temp file + fsync + rename + parent-dir fsync). Used only by
// the legacy migration and by rotation — NOT by the steady-state append path. It is durable
// for the same reason the per-record writers are (B2): the rotation/migration rewrite must
// not be lost or left torn by a crash any more than a credential write — otherwise a power
// loss right after a rotation could resurrect the just-trimmed prefix or expose a zero-length
// log. The steady-state append (AppendAudit) handles its own O_APPEND f.Sync separately.
func writeAuditJSONL(path string, entries []AuditEntry) error {
	var buf bytes.Buffer
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("controller: marshal %s: %w", auditFileName, err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if err := writeBytesDurable(path, buf.Bytes()); err != nil {
		return fmt.Errorf("controller: rewrite %s: %w", auditFileName, err)
	}
	return nil
}

// loadAuditTail returns the cached tail of a tenant's audit log, populating it on first
// use. On first use it migrates a legacy audit.json array to audit.jsonl (once), then
// reads the JSONL log to seed the last Seq/Hash + entry count. The caller must hold fs.mu.
func (fs *FileStore) loadAuditTail(t TenantID, dir string) (*auditTail, error) {
	if tail := fs.auditTails[t]; tail != nil {
		return tail, nil
	}
	jsonlPath := filepath.Join(dir, auditFileName)
	legacyPath := filepath.Join(dir, legacyAuditFileName)
	// Migrate a legacy array to JSONL once, BEFORE seeding the tail, so appends and
	// ListAudit never split across the two formats.
	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		var legacy []AuditEntry
		if err := readJSON(legacyPath, &legacy); err == nil {
			if werr := writeAuditJSONL(jsonlPath, legacy); werr != nil {
				return nil, werr
			}
			_ = os.Remove(legacyPath)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	entries, tornTail, err := readAuditJSONL(jsonlPath)
	if err != nil {
		return nil, err
	}
	if tornTail {
		// A crash left a partial trailing line. Rewrite the clean prefix so the next O_APPEND
		// lands on a well-formed file rather than concatenating onto the torn bytes (which
		// would turn the torn line into an unreadable interior line). One-time, under fs.mu.
		if werr := writeAuditJSONL(jsonlPath, entries); werr != nil {
			return nil, werr
		}
	}
	tail := &auditTail{count: len(entries)}
	if n := len(entries); n > 0 {
		tail.seq = entries[n-1].Seq
		tail.hash = entries[n-1].Hash
	}
	fs.auditTails[t] = tail
	return tail, nil
}

// rotateAudit trims the JSONL log down to the most-recent auditRetain entries and updates
// the cached count. It rewrites the whole file, but only runs once per
// (auditRotateAt-auditRetain) appends (amortized), so steady-state appends stay O(1). The
// caller must hold fs.mu and pass the tenant's cached tail.
func (fs *FileStore) rotateAudit(dir string, tail *auditTail) error {
	// A torn tail was already self-healed by loadAuditTail before any append, so the read
	// here sees a clean file; the tornTail flag is irrelevant at rotation time.
	entries, _, err := readAuditJSONL(filepath.Join(dir, auditFileName))
	if err != nil {
		return err
	}
	if len(entries) <= auditRetain {
		tail.count = len(entries)
		return nil
	}
	kept := entries[len(entries)-auditRetain:]
	if err := writeAuditJSONL(filepath.Join(dir, auditFileName), kept); err != nil {
		return err
	}
	tail.count = len(kept)
	return nil
}

// ================================ Audit ====================================

// AppendAudit appends an entry, chaining its PrevHash/Hash to the tenant's prior
// entry and assigning a monotonic Seq. The caller-provided Timestamp is
// preserved. Returns the stored entry with Seq/PrevHash/Hash set.
//
// The append is O(1): the next Seq + PrevHash come from the in-memory tail cache (seeded
// once via loadAuditTail), and the entry is written with a single O_APPEND line — no
// full-file read-modify-write per append (plan-6). The log is bounded by an amortized
// rotation that trims to auditRetain once it reaches auditRotateAt.
func (fs *FileStore) AppendAudit(ctx context.Context, t TenantID, e AuditEntry) (AuditEntry, error) {
	if err := ctx.Err(); err != nil {
		return AuditEntry{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return AuditEntry{}, err
	}
	tail, err := fs.loadAuditTail(t, dir)
	if err != nil {
		return AuditEntry{}, err
	}

	e.Seq = tail.seq + 1
	e = chainAudit(e, tail.hash)

	line, err := json.Marshal(e)
	if err != nil {
		return AuditEntry{}, fmt.Errorf("controller: marshal %s: %w", auditFileName, err)
	}
	line = append(line, '\n')
	f, err := os.OpenFile(filepath.Join(dir, auditFileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return AuditEntry{}, fmt.Errorf("controller: open %s: %w", auditFileName, err)
	}
	if _, werr := f.Write(line); werr != nil {
		_ = f.Close()
		return AuditEntry{}, fmt.Errorf("controller: append %s: %w", auditFileName, werr)
	}
	// fsync before close so the appended line is durable on stable storage (B2): the rotate
	// block below and the "durably appended" comment both assume the entry has actually hit
	// disk, not merely the page cache. Without this a crash right after a best-effort append
	// could silently drop the most recent audit row.
	if serr := f.Sync(); serr != nil {
		_ = f.Close()
		return AuditEntry{}, fmt.Errorf("controller: sync %s: %w", auditFileName, serr)
	}
	if cerr := f.Close(); cerr != nil {
		return AuditEntry{}, fmt.Errorf("controller: close %s: %w", auditFileName, cerr)
	}

	// Append committed — advance the cached tail, then rotate if we hit the high-water mark.
	tail.seq = e.Seq
	tail.hash = e.Hash
	tail.count++
	if tail.count > auditRotateAt {
		// The entry is already durably appended, so a rotation failure must neither lose it
		// nor fail the caller (many callers treat AppendAudit as best-effort). count stays
		// above the high-water mark, so the NEXT append retries rotation and self-heals; the
		// log just stays slightly over auditRetain until then. Caveat: while rotation keeps
		// failing (e.g. a full disk — writeAuditJSONL needs a momentary full temp copy), each
		// append re-reads + re-attempts the full rewrite, so the O(1) append degrades to O(N)
		// until space frees; it self-corrects once the rewrite succeeds.
		_ = fs.rotateAudit(dir, tail)
	}
	return e, nil
}

// ListAudit returns the tenant's audit entries in Seq order.
func (fs *FileStore) ListAudit(ctx context.Context, t TenantID) ([]AuditEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return nil, err
	}
	entries, err := fs.readAudit(dir)
	if err != nil {
		return nil, err
	}
	if entries == nil {
		return []AuditEntry{}, nil
	}
	return entries, nil
}
