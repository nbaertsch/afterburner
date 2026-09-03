package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func main() {
	working, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	repository := filepath.Clean(filepath.Join(working, "..", ".."))
	output := filepath.Join(working, "generated")
	if err := os.MkdirAll(output, 0o755); err != nil {
		panic(err)
	}
	for _, item := range []struct {
		source string
		target string
	}{
		{"BYOModels", "byo-models.zip"},
		{"BlackBox", "black-box.zip"},
	} {
		if err := createArchive(
			filepath.Join(repository, "extensions", item.source),
			filepath.Join(output, item.target),
		); err != nil {
			panic(err)
		}
	}
}

func createArchive(source, target string) error {
	var paths []string
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.IsDir() && excluded(entry.Name()) {
			return filepath.SkipDir
		}
		if !entry.IsDir() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(paths)
	temporary := target + ".tmp"
	file, err := os.Create(temporary)
	if err != nil {
		return err
	}
	writer := zip.NewWriter(file)
	for _, path := range paths {
		relative, _ := filepath.Rel(source, path)
		header := &zip.FileHeader{
			Name:   filepath.ToSlash(relative),
			Method: zip.Deflate,
		}
		header.SetModTime(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC))
		header.SetMode(0o600)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(entry, input)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(temporary, target); err != nil {
		return fmt.Errorf("activate %s: %w", target, err)
	}
	return nil
}

func excluded(name string) bool {
	switch strings.ToLower(name) {
	case ".git", "node_modules", "bin", "obj":
		return true
	default:
		return false
	}
}
