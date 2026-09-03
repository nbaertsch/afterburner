//go:build windows

package platform

import (
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

var (
	user32              = syscall.NewLazyDLL("user32.dll")
	sendMessageTimeoutW = user32.NewProc("SendMessageTimeoutW")
)

func EnsureUserPath(directory string) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open user environment registry: %w", err)
	}
	defer key.Close()
	value, valueType, err := key.GetStringValue("Path")
	if err != nil && err != registry.ErrNotExist {
		return fmt.Errorf("read user PATH: %w", err)
	}
	for _, entry := range filepath.SplitList(value) {
		if strings.EqualFold(filepath.Clean(entry), filepath.Clean(directory)) {
			return nil
		}
	}
	if value != "" && !strings.HasSuffix(value, ";") {
		value += ";"
	}
	value += directory
	if valueType == registry.EXPAND_SZ {
		err = key.SetExpandStringValue("Path", value)
	} else {
		err = key.SetStringValue("Path", value)
	}
	if err != nil {
		return fmt.Errorf("update user PATH: %w", err)
	}
	name, _ := syscall.UTF16PtrFromString("Environment")
	const (
		hwndBroadcast   = 0xffff
		wmSettingChange = 0x001A
		smtoAbortIfHung = 0x0002
	)
	sendMessageTimeoutW.Call(
		hwndBroadcast,
		wmSettingChange,
		0,
		uintptr(unsafe.Pointer(name)),
		smtoAbortIfHung,
		5000,
		0,
	)
	return nil
}
