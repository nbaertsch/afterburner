//go:build windows

package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

const (
	moveFileReplaceExisting = 0x1
	moveFileWriteThrough    = 0x8
)

var (
	kernel32    = syscall.NewLazyDLL("kernel32.dll")
	moveFileExW = kernel32.NewProc("MoveFileExW")
)

func ReplaceFile(source, destination string) error {
	return replaceFileLegacy(source, destination)
}

func pruneRetiredFiles(destination string) {
	matches, _ := filepath.Glob(filepath.Join(
		filepath.Dir(destination),
		"."+filepath.Base(destination)+".retired-*",
	))
	for _, path := range matches {
		_ = os.Remove(path)
	}
}

func RetiredFilePath(destination string) (string, error) {
	pruneRetiredFiles(destination)
	return retiredFilePath(destination)
}

func retiredFilePath(destination string) (string, error) {
	retired, err := os.CreateTemp(
		filepath.Dir(destination),
		"."+filepath.Base(destination)+".retired-*",
	)
	if err != nil {
		return "", err
	}
	retiredPath := retired.Name()
	if err := retired.Close(); err != nil {
		return "", err
	}
	if err := os.Remove(retiredPath); err != nil {
		return "", err
	}
	return retiredPath, nil
}

func ReplaceFileRetiring(source, destination, retiredPath string) error {
	if _, err := os.Stat(destination); os.IsNotExist(err) {
		return moveFile(source, destination, moveFileWriteThrough)
	} else if err != nil {
		return err
	}
	if filepath.Dir(retiredPath) != filepath.Dir(destination) {
		return fmt.Errorf("retired file must be in the destination directory")
	}
	if err := moveFile(destination, retiredPath, moveFileWriteThrough); err != nil {
		return err
	}
	if err := moveFile(source, destination, moveFileWriteThrough); err != nil {
		rollbackErr := moveFile(retiredPath, destination, moveFileWriteThrough)
		return errors.Join(err, rollbackErr)
	}
	_ = os.Remove(retiredPath)
	return nil
}

func replaceFileLegacy(source, destination string) error {
	return moveFile(source, destination, moveFileReplaceExisting|moveFileWriteThrough)
}

func moveFile(source, destination string, flags uintptr) error {
	sourcePointer, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPointer, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	result, _, callErr := moveFileExW.Call(
		uintptr(unsafe.Pointer(sourcePointer)),
		uintptr(unsafe.Pointer(destinationPointer)),
		flags,
	)
	if result == 0 {
		return fmt.Errorf("replace %s: %w", destination, callErr)
	}
	return nil
}
