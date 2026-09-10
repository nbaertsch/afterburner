//go:build !windows

package platform

import "os"

func ReplaceFile(source, destination string) error {
	return os.Rename(source, destination)
}

func RetiredFilePath(destination string) (string, error) {
	return destination + ".retired", nil
}

func ReplaceFileRetiring(source, destination, _ string) error {
	return ReplaceFile(source, destination)
}
