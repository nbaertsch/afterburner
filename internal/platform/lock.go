package platform

import (
	"fmt"
	"os"
	"time"
)

func AcquireDirectoryLock(path string, timeout time.Duration) (func(), error) {
	deadline := time.Now().Add(timeout)
	for {
		err := os.Mkdir(path, 0o700)
		if err == nil {
			return func() { _ = os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("acquire transaction lock: %w", err)
		}
		info, statErr := os.Stat(path)
		if statErr == nil && time.Since(info.ModTime()) > 5*time.Minute {
			if removeErr := os.Remove(path); removeErr == nil || os.IsNotExist(removeErr) {
				continue
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for transaction lock")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
