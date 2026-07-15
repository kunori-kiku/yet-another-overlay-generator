package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type tarFixture struct {
	name     string
	typeflag byte
	body     string
	linkname string
}

func TestAuditAndExtractPositiveTarAndZip(t *testing.T) {
	for _, format := range []string{"tar.gz", "zip"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			archive := filepath.Join(root, "bundle."+format)
			entries := []tarFixture{
				{name: "bin/", typeflag: tar.TypeDir},
				{name: "bin/yaog-agent", typeflag: tar.TypeReg, body: "agent"},
				{name: "frontend/", typeflag: tar.TypeDir},
				{name: "frontend/index.html", typeflag: tar.TypeReg, body: "index"},
			}
			if format == "tar.gz" {
				writeTarFixture(t, archive, entries)
			} else {
				writeZipFixture(t, archive, entries)
			}
			extract := filepath.Join(root, "extract")
			if err := auditAndExtract(archive, extract, []string{"bin/yaog-agent", "frontend/index.html"}); err != nil {
				t.Fatalf("auditAndExtract: %v", err)
			}
			got, err := os.ReadFile(filepath.Join(extract, "bin", "yaog-agent"))
			if err != nil || string(got) != "agent" {
				t.Fatalf("extracted agent = %q, %v", got, err)
			}
		})
	}
}

func TestCanonicalMemberNameRejectsAliases(t *testing.T) {
	for _, name := range []string{
		"", "./bin/agent", "bin/./agent", "bin/../agent", "../agent",
		"/bin/agent", "C:/bin/agent", `bin\agent`, "bin//agent", "bin/agent/",
		"bin\nagent", "fröntend/index.html", "bin/foo:bar", "bin/agent.", "bin/agent ",
		"CON", "bin/NUL.txt", "bin/com1.exe", "frontend/LPT9.css",
	} {
		t.Run(strings.ReplaceAll(name, "/", "_"), func(t *testing.T) {
			if _, err := canonicalMemberName(name, false); err == nil {
				t.Fatalf("canonicalMemberName(%q) succeeded", name)
			}
		})
	}
}

func TestValidateMembersRejectsDuplicateCaseAndPrefixAliases(t *testing.T) {
	tests := map[string][]member{
		"duplicate": {
			{name: "bin/agent", kind: kindFile, size: 1},
			{name: "bin/agent", kind: kindFile, size: 1},
		},
		"case fold": {
			{name: "frontend/yaog.wasm", kind: kindFile, size: 1},
			{name: "frontend/YAOG.WASM", kind: kindFile, size: 1},
		},
		"file before child": {
			{name: "frontend", kind: kindFile, size: 1},
			{name: "frontend/index.html", kind: kindFile, size: 1},
		},
		"file after child": {
			{name: "frontend/index.html", kind: kindFile, size: 1},
			{name: "frontend", kind: kindFile, size: 1},
		},
	}
	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := validateMembers(entries); err == nil {
				t.Fatal("validateMembers succeeded")
			}
		})
	}
}

func TestTarRejectsLinksAndSpecialTypes(t *testing.T) {
	types := map[string]byte{
		"symlink":          tar.TypeSymlink,
		"hardlink":         tar.TypeLink,
		"character-device": tar.TypeChar,
		"block-device":     tar.TypeBlock,
		"fifo":             tar.TypeFifo,
	}
	for name, typeflag := range types {
		t.Run(name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "bad.tar.gz")
			writeTarFixture(t, archive, []tarFixture{{name: "bad", typeflag: typeflag, linkname: "target"}})
			if _, err := listTarGz(archive); err == nil {
				t.Fatal("listTarGz accepted special member")
			}
		})
	}
}

func TestTarRejectsTraversalAndRepeatedSeparator(t *testing.T) {
	for _, name := range []string{"../outside", "bin//agent", "./bin/agent"} {
		t.Run(name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "bad.tar.gz")
			writeTarFixture(t, archive, []tarFixture{{name: name, typeflag: tar.TypeReg, body: "bad"}})
			members, err := listTarGz(archive)
			if err != nil {
				t.Fatalf("listTarGz setup: %v", err)
			}
			if _, err := validateMembers(members); err == nil {
				t.Fatal("validateMembers accepted unsafe path")
			}
		})
	}
}

func TestZipRejectsSymlinkCaseAndPrefixAliases(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		archive := filepath.Join(t.TempDir(), "bad.zip")
		writeZipFixture(t, archive, []tarFixture{{name: "escape-link", typeflag: tar.TypeSymlink, body: "../../outside"}})
		if _, err := listZip(archive); err == nil {
			t.Fatal("listZip accepted symlink")
		}
	})
	for name, entries := range map[string][]tarFixture{
		"case": {
			{name: "frontend/yaog.wasm", typeflag: tar.TypeReg, body: "a"},
			{name: "frontend/YAOG.WASM", typeflag: tar.TypeReg, body: "b"},
		},
		"prefix": {
			{name: "frontend/index.html", typeflag: tar.TypeReg, body: "a"},
			{name: "frontend", typeflag: tar.TypeReg, body: "b"},
		},
		"repeated separator": {{name: "bin//agent", typeflag: tar.TypeReg, body: "a"}},
	} {
		t.Run(name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "bad.zip")
			writeZipFixture(t, archive, entries)
			members, err := listZip(archive)
			if err != nil {
				t.Fatalf("listZip setup: %v", err)
			}
			if _, err := validateMembers(members); err == nil {
				t.Fatal("validateMembers accepted alias")
			}
		})
	}
}

func TestRequiredMemberMustBeRegular(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "bundle.zip")
	writeZipFixture(t, archive, []tarFixture{{name: "bin/", typeflag: tar.TypeDir}})
	if err := auditAndExtract(archive, filepath.Join(root, "extract"), []string{"bin"}); err == nil {
		t.Fatal("auditAndExtract accepted a directory as a required regular member")
	}
}

func TestResourceBounds(t *testing.T) {
	t.Run("member count", func(t *testing.T) {
		entries := make([]member, maxMemberCount+1)
		for i := range entries {
			entries[i] = member{name: "f/" + strings.Repeat("a", i%50) + string(rune('A'+i%26)), kind: kindFile}
		}
		if _, err := validateMembers(entries); err == nil || !strings.Contains(err.Error(), "limit") {
			t.Fatalf("validateMembers error = %v", err)
		}
	})
	t.Run("expanded total", func(t *testing.T) {
		entries := []member{
			{name: "one", kind: kindFile, size: maxMemberSize},
			{name: "two", kind: kindFile, size: maxMemberSize},
			{name: "three", kind: kindFile, size: maxMemberSize},
			{name: "four", kind: kindFile, size: maxMemberSize},
			{name: "five", kind: kindFile, size: 1},
		}
		if _, err := validateMembers(entries); err == nil || !strings.Contains(err.Error(), "expands past") {
			t.Fatalf("validateMembers error = %v", err)
		}
	})
	t.Run("outer size", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "huge.zip")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(maxOuterSize + 1); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if _, cleanup, err := snapshotArchive(path); err == nil {
			cleanup()
			t.Fatal("snapshotArchive accepted oversized outer archive")
		}
	})
	t.Run("outer symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target.zip")
		if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "link.zip")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, cleanup, err := snapshotArchive(link); err == nil {
			cleanup()
			t.Fatal("snapshotArchive accepted outer symlink")
		}
	})
}

func writeTarFixture(t *testing.T, path string, entries []tarFixture) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		size := int64(len(entry.body))
		if entry.typeflag == tar.TypeDir || entry.typeflag == tar.TypeSymlink || entry.typeflag == tar.TypeLink || entry.typeflag == tar.TypeChar || entry.typeflag == tar.TypeBlock || entry.typeflag == tar.TypeFifo {
			size = 0
		}
		hdr := &tar.Header{Name: entry.name, Typeflag: entry.typeflag, Mode: 0o755, Size: size, Linkname: entry.linkname}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if size != 0 {
			if _, err := tw.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeZipFixture(t *testing.T, path string, entries []tarFixture) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, entry := range entries {
		hdr := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		switch entry.typeflag {
		case tar.TypeDir:
			hdr.SetMode(os.ModeDir | 0o755)
		case tar.TypeSymlink:
			hdr.SetMode(os.ModeSymlink | 0o777)
		default:
			hdr.SetMode(0o755)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if entry.body != "" {
			if _, err := w.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
