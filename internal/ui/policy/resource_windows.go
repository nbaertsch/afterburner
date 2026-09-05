//go:build windows

package policy

import (
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func PlatformIsolationCapabilities() IsolationCapabilities {
	return IsolationCapabilities{
		Platform:               runtime.GOOS,
		RestrictedEnvironment:  true,
		ProcessTreeContainment: true,
		ResourceLimits:         true,
		FilesystemBoundary:     false,
		NetworkBoundary:        false,
	}
}

func ApplyWindowsJobObjectPolicy(job windows.Handle, policy ResourcePolicy) error {
	policy.NormalizeDurations()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if !policy.KillOnClose {
		limits.BasicLimitInformation.LimitFlags = 0
	}
	if policy.MaxProcesses > 0 {
		limits.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
		limits.BasicLimitInformation.ActiveProcessLimit = policy.MaxProcesses
	}
	if policy.MaxProcessMemory > 0 {
		limits.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY
		limits.ProcessMemoryLimit = uintptr(policy.MaxProcessMemory)
	}
	if policy.MaxJobMemory > 0 {
		limits.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_JOB_MEMORY
		limits.JobMemoryLimit = uintptr(policy.MaxJobMemory)
	}
	if policy.MaxCPUTime > 0 {
		limits.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_JOB_TIME
		limits.BasicLimitInformation.PerJobUserTimeLimit = int64(policy.MaxCPUTime / (100 * time.Nanosecond))
	}
	_, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	return err
}
