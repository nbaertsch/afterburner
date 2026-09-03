//go:build !windows

package platform

import "fmt"

func EnsureUserPath(directory string) error {
	return fmt.Errorf("automatic PATH installation is not supported on this platform: %s", directory)
}
