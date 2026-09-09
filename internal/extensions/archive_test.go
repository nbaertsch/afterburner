package extensions

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type archiveTestEntry struct {
	name string
	mode os.FileMode
	data string
}

func TestExtractPackageArchiveRejectsUnsafeEntries(t *testing.T) {
	tests := map[string][]archiveTestEntry{
		"traversal": {
			{name: "../escape.txt", data: "bad"},
		},
		"case collision": {
			{name: "runtime.mjs", data: "one"},
			{name: "RUNTIME.MJS", data: "two"},
		},
		"symlink": {
			{name: "runtime.mjs", mode: os.ModeSymlink | 0o777, data: "target"},
		},
	}
	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			err := ExtractPackageArchive(makeArchive(t, entries), t.TempDir())
			if err == nil {
				t.Fatal("expected unsafe archive to be rejected")
			}
		})
	}
}

func TestExtractPackageArchiveEnforcesBounds(t *testing.T) {
	archive := makeArchive(t, []archiveTestEntry{
		{name: "one.txt", data: "1234"},
		{name: "two.txt", data: "5678"},
	})
	tests := map[string]packageArchiveLimits{
		"archive":  {archiveBytes: len(archive) - 1, entryBytes: 100, expandedBytes: 100, entries: 10},
		"entry":    {archiveBytes: len(archive), entryBytes: 3, expandedBytes: 100, entries: 10},
		"expanded": {archiveBytes: len(archive), entryBytes: 100, expandedBytes: 7, entries: 10},
		"count":    {archiveBytes: len(archive), entryBytes: 100, expandedBytes: 100, entries: 1},
	}
	for name, limits := range tests {
		t.Run(name, func(t *testing.T) {
			if err := extractPackageArchive(archive, t.TempDir(), limits); err == nil {
				t.Fatal("expected archive bound to be enforced")
			}
		})
	}
}

func TestExtractPackageArchiveWritesRegularFiles(t *testing.T) {
	destination := t.TempDir()
	archive := makeArchive(t, []archiveTestEntry{
		{name: "afterburner.json", data: "{}"},
		{name: "lib/runtime.mjs", data: "export default {}"},
	})
	if err := ExtractPackageArchive(archive, destination); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(destination, "lib", "runtime.mjs"))
	if err != nil || string(content) != "export default {}" {
		t.Fatalf("extracted content = %q, %v", content, err)
	}
}

func makeArchive(t *testing.T, entries []archiveTestEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		output, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := output.Write([]byte(entry.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if buffer.Len() == 0 || strings.TrimSpace(buffer.String()) == "" {
		t.Fatal("archive was empty")
	}
	return buffer.Bytes()
}
