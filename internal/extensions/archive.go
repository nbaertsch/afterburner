package extensions

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nbaertsch/afterburner/internal/registry"
)

const (
	maxPackageArchiveBytes   = 64 << 20
	maxPackageEntryBytes     = 32 << 20
	maxPackageExpandedBytes  = 256 << 20
	maxPackageArchiveEntries = 2048
)

type packageArchiveLimits struct {
	archiveBytes  int
	entryBytes    uint64
	expandedBytes uint64
	entries       int
}

var defaultPackageArchiveLimits = packageArchiveLimits{
	archiveBytes:  maxPackageArchiveBytes,
	entryBytes:    maxPackageEntryBytes,
	expandedBytes: maxPackageExpandedBytes,
	entries:       maxPackageArchiveEntries,
}

func ExtractPackageArchive(data []byte, destination string) error {
	return extractPackageArchive(data, destination, defaultPackageArchiveLimits)
}

func extractPackageArchive(data []byte, destination string, limits packageArchiveLimits) error {
	if len(data) == 0 || len(data) > limits.archiveBytes {
		return fmt.Errorf("extension archive size must be between 1 byte and %d bytes", limits.archiveBytes)
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("open extension archive: %w", err)
	}
	if len(reader.File) == 0 || len(reader.File) > limits.entries {
		return fmt.Errorf("extension archive contains an invalid number of entries")
	}
	seen := make(map[string]struct{}, len(reader.File))
	var expanded uint64
	for _, file := range reader.File {
		relative := filepath.Clean(filepath.FromSlash(file.Name))
		target := filepath.Join(destination, relative)
		key := strings.ToLower(filepath.ToSlash(relative))
		if relative == "." || filepath.IsAbs(relative) || !filepath.IsLocal(relative) ||
			!registry.Within(target, destination) {
			return fmt.Errorf("extension archive entry %q escapes the extraction root", file.Name)
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("extension archive contains duplicate path %q", file.Name)
		}
		seen[key] = struct{}{}
		info := file.FileInfo()
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("extension archive contains unsupported entry %q", file.Name)
		}
		if info.IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if file.UncompressedSize64 > limits.entryBytes ||
			file.UncompressedSize64 > limits.expandedBytes ||
			expanded > limits.expandedBytes-file.UncompressedSize64 {
			return fmt.Errorf("extension archive exceeds expanded size limits")
		}
		expanded += file.UncompressedSize64
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		input, err := file.Open()
		if err != nil {
			return err
		}
		content, readErr := io.ReadAll(io.LimitReader(input, int64(limits.entryBytes)+1))
		closeErr := input.Close()
		if readErr != nil || closeErr != nil || uint64(len(content)) != file.UncompressedSize64 {
			return fmt.Errorf("read extension archive entry %q", file.Name)
		}
		if err := os.WriteFile(target, content, 0o600); err != nil {
			return err
		}
	}
	return nil
}
