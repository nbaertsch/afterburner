//go:build windows

package platform

import (
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type ProcessInfo struct {
	PID  int
	Name string
	Path string
}

func RunningExecutables(names []string, excludePID int) ([]ProcessInfo, error) {
	wanted := map[string]struct{}{}
	for _, name := range names {
		wanted[strings.ToLower(name)] = struct{}{}
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, err
	}
	var processes []ProcessInfo
	for {
		name := windows.UTF16ToString(entry.ExeFile[:])
		if _, ok := wanted[strings.ToLower(name)]; ok && int(entry.ProcessID) != excludePID {
			info := ProcessInfo{PID: int(entry.ProcessID), Name: name}
			if path, err := processImagePath(entry.ProcessID); err == nil {
				info.Path = path
			}
			processes = append(processes, info)
		}
		err = windows.Process32Next(snapshot, &entry)
		if err == windows.ERROR_NO_MORE_FILES {
			break
		}
		if err != nil {
			return processes, err
		}
	}
	return processes, nil
}

func processImagePath(pid uint32) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	buffer := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return "", err
	}
	return filepath.Clean(windows.UTF16ToString(buffer[:size])), nil
}

func FormatProcessList(processes []ProcessInfo) string {
	parts := make([]string, 0, len(processes))
	for _, process := range processes {
		if process.Path != "" {
			parts = append(parts, fmt.Sprintf("%s PID %d (%s)", process.Name, process.PID, process.Path))
		} else {
			parts = append(parts, fmt.Sprintf("%s PID %d", process.Name, process.PID))
		}
	}
	return strings.Join(parts, "; ")
}
