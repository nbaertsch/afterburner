//go:build !windows

package policy

import "runtime"

func PlatformIsolationCapabilities() IsolationCapabilities {
	return IsolationCapabilities{
		Platform:              runtime.GOOS,
		RestrictedEnvironment: true,
		FilesystemBoundary:    false,
		NetworkBoundary:       false,
	}
}

func ApplyWindowsJobObjectPolicy(_ uintptr, _ ResourcePolicy) error { return nil }
