// Command release_archive_audit validates a release archive without trusting an
// extractor's path normalization or link handling. After the complete archive is
// structurally and cryptographically read, it extracts only explicitly required
// regular files into a new directory.
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const (
	maxMemberSize  = 256 << 20
	maxArchiveSize = 1 << 30
	maxOuterSize   = 512 << 20
	maxMemberCount = 100_000
)

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

type memberKind byte

const (
	kindFile memberKind = 'f'
	kindDir  memberKind = 'd'
)

type member struct {
	name string
	kind memberKind
	size int64
	mode os.FileMode
}

func main() {
	var archivePath string
	var extractDir string
	var manifestPath string
	var required stringList
	flag.StringVar(&archivePath, "archive", "", "archive to audit (.tar.gz or .zip)")
	flag.StringVar(&extractDir, "extract-dir", "", "new directory for required regular files")
	flag.StringVar(&manifestPath, "manifest", "", "write a complete audited member manifest")
	flag.Var(&required, "require", "required regular member (repeatable)")
	flag.Parse()
	if flag.NArg() != 0 || archivePath == "" || extractDir == "" || manifestPath == "" || len(required) == 0 {
		fmt.Fprintln(os.Stderr, "usage: go run scripts/release_archive_audit.go -archive FILE -extract-dir DIR -manifest FILE -require MEMBER [...]")
		os.Exit(2)
	}
	if err := auditExtractAndManifest(archivePath, extractDir, manifestPath, required); err != nil {
		fmt.Fprintf(os.Stderr, "archive audit failed for %s: %v\n", filepath.Base(archivePath), err)
		os.Exit(1)
	}
}

func auditAndExtract(archivePath, extractDir string, required []string) error {
	manifest, err := os.CreateTemp("", "yaog-release-member-manifest-*")
	if err != nil {
		return err
	}
	manifestPath := manifest.Name()
	if err := manifest.Close(); err != nil {
		return err
	}
	_ = os.Remove(manifestPath)
	defer os.Remove(manifestPath)
	return auditExtractAndManifest(archivePath, extractDir, manifestPath, required)
}

func auditExtractAndManifest(archivePath, extractDir, manifestPath string, required []string) error {
	if info, err := os.Lstat(extractDir); err == nil {
		return fmt.Errorf("extract directory already exists (%s, mode %s)", extractDir, info.Mode())
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	// Capture one bounded private snapshot from a single opened inode. All
	// structural, payload, and extraction passes use it, so replacing the source
	// path between passes cannot replace the bytes being verified.
	snapshot, cleanup, err := snapshotArchive(archivePath)
	if err != nil {
		return err
	}
	defer cleanup()

	var members []member
	switch {
	case strings.HasSuffix(archivePath, ".tar.gz"):
		members, err = listTarGz(snapshot)
	case strings.HasSuffix(archivePath, ".zip"):
		members, err = listZip(snapshot)
	default:
		return errors.New("unsupported archive suffix")
	}
	if err != nil {
		return err
	}
	byName, err := validateMembers(members)
	if err != nil {
		return err
	}
	for _, name := range required {
		canonical, err := canonicalMemberName(name, false)
		if err != nil || canonical != name {
			return fmt.Errorf("invalid required member %q", name)
		}
		entry, ok := byName[strings.ToLower(name)]
		if !ok {
			return fmt.Errorf("required member %q is missing", name)
		}
		if entry.name != name || entry.kind != kindFile {
			return fmt.Errorf("required member %q is not one exact-case regular file", name)
		}
	}

	// Read every payload before writing anything. archive/zip verifies CRCs while
	// reading, and the tar/gzip readers verify stream integrity. Size bounds make
	// this a bounded archive-bomb check as well as an integrity pass.
	digests, err := hashAllPayloads(snapshot)
	if err != nil {
		return err
	}
	if err := writeManifest(manifestPath, members, digests); err != nil {
		return err
	}
	if err := os.Mkdir(extractDir, 0o700); err != nil {
		return err
	}
	if err := extractRequired(snapshot, extractDir, required); err != nil {
		_ = os.RemoveAll(extractDir)
		return err
	}
	return nil
}

func snapshotArchive(path string) (string, func(), error) {
	outer, err := os.Lstat(path)
	if err != nil {
		return "", func() {}, err
	}
	if !outer.Mode().IsRegular() || outer.Mode()&os.ModeSymlink != 0 {
		return "", func() {}, errors.New("outer archive is not a regular non-symlink file")
	}
	if outer.Size() <= 0 || outer.Size() > maxOuterSize {
		return "", func() {}, fmt.Errorf("outer archive size %d is outside 1..%d", outer.Size(), maxOuterSize)
	}
	source, err := os.Open(path)
	if err != nil {
		return "", func() {}, err
	}
	opened, err := source.Stat()
	if err != nil {
		source.Close()
		return "", func() {}, err
	}
	if !os.SameFile(outer, opened) {
		source.Close()
		return "", func() {}, errors.New("outer archive changed while it was opened")
	}
	suffix := ".zip"
	if strings.HasSuffix(path, ".tar.gz") {
		suffix = ".tar.gz"
	}
	tmp, err := os.CreateTemp("", "yaog-release-archive-snapshot-*"+suffix)
	if err != nil {
		source.Close()
		return "", func() {}, err
	}
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}
	n, copyErr := io.Copy(tmp, io.LimitReader(source, maxOuterSize+1))
	closeErr := source.Close()
	if copyErr != nil || closeErr != nil || n != outer.Size() || n > maxOuterSize {
		cleanup()
		if copyErr != nil {
			return "", func() {}, copyErr
		}
		if closeErr != nil {
			return "", func() {}, closeErr
		}
		return "", func() {}, errors.New("outer archive changed size while it was snapshotted")
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return tmp.Name(), cleanup, nil
}

func canonicalMemberName(raw string, directory bool) (string, error) {
	if raw == "" {
		return "", errors.New("empty member name")
	}
	if len(raw) > 4096 {
		return "", errors.New("member name exceeds 4096 bytes")
	}
	for _, r := range raw {
		if r > unicode.MaxASCII || unicode.IsControl(r) {
			return "", fmt.Errorf("non-ASCII or control character in member %q", raw)
		}
	}
	if strings.Contains(raw, `\`) {
		return "", fmt.Errorf("backslash in member %q", raw)
	}
	if strings.HasPrefix(raw, "/") || (len(raw) >= 2 && isASCIILetter(raw[0]) && raw[1] == ':') {
		return "", fmt.Errorf("absolute or drive-qualified member %q", raw)
	}
	if strings.Contains(raw, "//") {
		return "", fmt.Errorf("repeated separator in member %q", raw)
	}
	name := raw
	if directory {
		if !strings.HasSuffix(name, "/") {
			return "", fmt.Errorf("directory member lacks trailing slash: %q", raw)
		}
		name = strings.TrimSuffix(name, "/")
	} else if strings.HasSuffix(name, "/") {
		return "", fmt.Errorf("regular member has trailing slash: %q", raw)
	}
	if name == "" {
		return "", fmt.Errorf("archive root record is not allowed: %q", raw)
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("empty, dot, or traversal segment in member %q", raw)
		}
		if len(segment) > 255 || strings.Contains(segment, ":") || strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, " ") {
			return "", fmt.Errorf("Windows-ambiguous segment in member %q", raw)
		}
		base := strings.ToLower(strings.SplitN(segment, ".", 2)[0])
		if base == "con" || base == "prn" || base == "aux" || base == "nul" ||
			base == "com1" || base == "com2" || base == "com3" || base == "com4" ||
			base == "com5" || base == "com6" || base == "com7" || base == "com8" || base == "com9" ||
			base == "lpt1" || base == "lpt2" || base == "lpt3" || base == "lpt4" ||
			base == "lpt5" || base == "lpt6" || base == "lpt7" || base == "lpt8" || base == "lpt9" {
			return "", fmt.Errorf("Windows-reserved segment in member %q", raw)
		}
	}
	return name, nil
}

func isASCIILetter(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func validateMembers(members []member) (map[string]member, error) {
	if len(members) == 0 {
		return nil, errors.New("archive has no members")
	}
	if len(members) > maxMemberCount {
		return nil, fmt.Errorf("archive has %d members, limit is %d", len(members), maxMemberCount)
	}
	byName := make(map[string]member, len(members))
	var total int64
	for _, entry := range members {
		if entry.kind != kindFile && entry.kind != kindDir {
			return nil, fmt.Errorf("member %q has a forbidden special type", entry.name)
		}
		canonical, err := canonicalMemberName(entry.name, entry.kind == kindDir)
		if err != nil {
			return nil, err
		}
		entry.name = canonical
		if entry.size < 0 || entry.size > maxMemberSize {
			return nil, fmt.Errorf("member %q has unsafe size %d", canonical, entry.size)
		}
		if entry.size > maxArchiveSize-total {
			return nil, fmt.Errorf("archive expands past %d bytes", maxArchiveSize)
		}
		total += entry.size
		key := strings.ToLower(canonical)
		if prior, exists := byName[key]; exists {
			return nil, fmt.Errorf("duplicate or case-fold-colliding members %q and %q", prior.name, canonical)
		}
		byName[key] = entry
	}
	for key, entry := range byName {
		parts := strings.Split(key, "/")
		for i := 1; i < len(parts); i++ {
			ancestor := strings.Join(parts[:i], "/")
			if prior, exists := byName[ancestor]; exists && prior.kind == kindFile {
				return nil, fmt.Errorf("file/directory prefix collision between %q and %q", prior.name, entry.name)
			}
		}
	}
	return byName, nil
}

func listTarGz(path string) ([]member, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var result []member
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		kind := memberKind(0)
		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			kind = kindFile
		case tar.TypeDir:
			kind = kindDir
		default:
			return nil, fmt.Errorf("member %q has forbidden tar type %q", hdr.Name, hdr.Typeflag)
		}
		result = append(result, member{name: hdr.Name, kind: kind, size: hdr.Size, mode: hdr.FileInfo().Mode()})
		if len(result) > maxMemberCount {
			return nil, fmt.Errorf("archive exceeds %d members", maxMemberCount)
		}
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return result, nil
}

func listZip(path string) ([]member, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	result := make([]member, 0, len(zr.File))
	for _, zf := range zr.File {
		mode := zf.Mode()
		kind := memberKind(0)
		switch {
		case mode.IsRegular() && !strings.HasSuffix(zf.Name, "/"):
			kind = kindFile
		case mode.IsDir() && strings.HasSuffix(zf.Name, "/"):
			kind = kindDir
		default:
			return nil, fmt.Errorf("member %q has forbidden ZIP mode %s", zf.Name, mode)
		}
		if zf.UncompressedSize64 > uint64(maxMemberSize) {
			return nil, fmt.Errorf("member %q has unsafe size %d", zf.Name, zf.UncompressedSize64)
		}
		result = append(result, member{name: zf.Name, kind: kind, size: int64(zf.UncompressedSize64), mode: mode})
		if len(result) > maxMemberCount {
			return nil, fmt.Errorf("archive exceeds %d members", maxMemberCount)
		}
	}
	return result, nil
}

func hashAllPayloads(archivePath string) (map[string]string, error) {
	digests := make(map[string]string)
	if strings.HasSuffix(archivePath, ".zip") {
		zr, err := zip.OpenReader(archivePath)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		for _, zf := range zr.File {
			if zf.Mode().IsDir() {
				continue
			}
			r, err := zf.Open()
			if err != nil {
				return nil, err
			}
			hash := sha256.New()
			n, copyErr := io.Copy(hash, io.LimitReader(r, maxMemberSize+1))
			closeErr := r.Close()
			if copyErr != nil {
				return nil, copyErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
			if n != int64(zf.UncompressedSize64) {
				return nil, fmt.Errorf("member %q yielded %d bytes, expected %d", zf.Name, n, zf.UncompressedSize64)
			}
			digests[zf.Name] = fmt.Sprintf("%x", hash.Sum(nil))
		}
		return digests, nil
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag == tar.TypeReg || hdr.Typeflag == tar.TypeRegA {
			hash := sha256.New()
			n, err := io.Copy(hash, io.LimitReader(tr, maxMemberSize+1))
			if err != nil {
				return nil, err
			}
			if n != hdr.Size {
				return nil, fmt.Errorf("member %q yielded %d bytes, expected %d", hdr.Name, n, hdr.Size)
			}
			digests[hdr.Name] = fmt.Sprintf("%x", hash.Sum(nil))
		}
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return digests, nil
}

func writeManifest(path string, members []member, digests map[string]string) error {
	sorted := append([]member(nil), members...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	for _, entry := range sorted {
		canonical, err := canonicalMemberName(entry.name, entry.kind == kindDir)
		if err != nil {
			f.Close()
			return err
		}
		digest := "-"
		if entry.kind == kindFile {
			digest = digests[entry.name]
			if len(digest) != 64 {
				f.Close()
				return fmt.Errorf("missing digest for %q", entry.name)
			}
		}
		if _, err := fmt.Fprintf(f, "%c\t%04o\t%d\t%s\t%s\n", entry.kind, entry.mode.Perm(), entry.size, digest, canonical); err != nil {
			f.Close()
			return err
		}
	}
	return f.Close()
}

func extractRequired(archivePath, extractDir string, required []string) error {
	wanted := make(map[string]struct{}, len(required))
	for _, name := range required {
		wanted[name] = struct{}{}
	}
	if strings.HasSuffix(archivePath, ".zip") {
		zr, err := zip.OpenReader(archivePath)
		if err != nil {
			return err
		}
		defer zr.Close()
		for _, zf := range zr.File {
			if _, ok := wanted[zf.Name]; !ok {
				continue
			}
			r, err := zf.Open()
			if err != nil {
				return err
			}
			if err := writeRequired(extractDir, zf.Name, r, int64(zf.UncompressedSize64)); err != nil {
				r.Close()
				return err
			}
			if err := r.Close(); err != nil {
				return err
			}
			delete(wanted, zf.Name)
		}
	} else {
		f, err := os.Open(archivePath)
		if err != nil {
			return err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		for {
			hdr, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			if _, ok := wanted[hdr.Name]; !ok {
				continue
			}
			if err := writeRequired(extractDir, hdr.Name, tr, hdr.Size); err != nil {
				return err
			}
			delete(wanted, hdr.Name)
		}
	}
	if len(wanted) != 0 {
		missing := make([]string, 0, len(wanted))
		for name := range wanted {
			missing = append(missing, name)
		}
		sort.Strings(missing)
		return fmt.Errorf("required members disappeared during extraction: %s", strings.Join(missing, ", "))
	}
	return nil
}

func writeRequired(root, name string, source io.Reader, expected int64) error {
	destination := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(f, io.LimitReader(source, maxMemberSize+1))
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != expected {
		return fmt.Errorf("required member %q yielded %d bytes, expected %d", name, n, expected)
	}
	if n == 0 {
		return fmt.Errorf("required member %q is empty", name)
	}
	return nil
}
