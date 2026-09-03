//go:build !windows

package platform

import "os"

func ReplaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
