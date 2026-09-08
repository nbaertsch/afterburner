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
		{"OpenAIServer", "openai-server.zip"},
	} {
		if err := createArchive(
			filepath.Join(repository, "extensions", item.source),
			filepath.Join(output, item.target),
		); err != nil {
			panic(err)
		}
	}
}

type archiveEntry struct {
	path string
	name string
}

func createArchive(source, target string) error {
	var entries []archiveEntry
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name, err := archiveName(source, path)
		if err != nil {
			return err
		}
		if name == "" {
			return nil
		}
		if excluded(entry.Name(), entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !entry.IsDir() {
			if entry.Type()&os.ModeType != 0 {
				return nil
			}
			entries = append(entries, archiveEntry{path: path, name: name})
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	temporary := target + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	writer := zip.NewWriter(file)
	for _, item := range entries {
		header := &zip.FileHeader{
			Name:   item.name,
			Method: zip.Deflate,
		}
		header.SetModTime(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC))
		header.SetMode(0o600)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		input, err := os.Open(item.path)
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

func archiveName(source, path string) (string, error) {
	relative, err := filepath.Rel(source, path)
	if err != nil {
		return "", err
	}
	if relative == "." {
		return "", nil
	}
	if !filepath.IsLocal(relative) {
		return "", fmt.Errorf("refusing unsafe archive path %q", relative)
	}
	name := filepath.ToSlash(relative)
	if name == ".." || strings.HasPrefix(name, "../") || strings.HasPrefix(name, "/") || strings.ContainsRune(name, '\x00') {
		return "", fmt.Errorf("refusing unsafe archive path %q", relative)
	}
	return name, nil
}

func excluded(name string, isDir bool) bool {
	lower := strings.ToLower(name)
	if isExcludedTransient(lower, isDir) {
		return true
	}
	if !strings.HasPrefix(name, ".") {
		return false
	}
	return isDir || !allowedDotfile(lower)
}

func isExcludedTransient(name string, isDir bool) bool {
	switch name {
	case ".git", ".hg", ".svn", ".test-work", ".cache", ".logs", ".parcel-cache", ".nyc_output", "node_modules", "bin", "obj", "coverage", "logs", "test-results", "playwright-report", "tmp", "temp":
		return true
	case ".ds_store", "thumbs.db", "npm-debug.log", "yarn-debug.log", "yarn-error.log", "pnpm-debug.log":
		return true
	}
	if isDir {
		return false
	}
	return strings.HasSuffix(name, ".log") ||
		strings.HasSuffix(name, ".tmp") ||
		strings.HasSuffix(name, ".temp") ||
		strings.HasSuffix(name, ".swp") ||
		strings.HasSuffix(name, ".pid") ||
		strings.HasSuffix(name, ".lcov") ||
		strings.HasSuffix(name, "~")
}

func allowedDotfile(name string) bool {
	switch name {
	case ".editorconfig", ".eslintignore", ".eslintrc", ".eslintrc.cjs", ".eslintrc.json", ".gitignore", ".node-version", ".npmrc", ".nvmrc", ".prettierignore", ".prettierrc", ".prettierrc.json", ".prettierrc.yaml", ".prettierrc.yml", ".yarnrc", ".yarnrc.yml":
		return true
	default:
		return false
	}
}
