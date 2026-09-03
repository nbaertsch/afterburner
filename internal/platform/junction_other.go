//go:build !windows

package platform

import "os"

func CreateDirectoryLink(source, target string) error {
	return os.Symlink(source, target)
}
