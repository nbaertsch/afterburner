//go:build windows

package platform

import (
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unicode/utf16"
)

const (
	ioReparseTagMountPoint   = 0xA0000003
	fsctlSetReparsePoint     = 0x000900A4
	fileFlagOpenReparsePoint = 0x00200000
	fileFlagBackupSemantics  = 0x02000000
)

func CreateDirectoryLink(source, target string) error {
	if err := os.Mkdir(target, 0o700); err != nil {
		return err
	}
	success := false
	defer func() {
		if !success {
			_ = os.Remove(target)
		}
	}()
	targetPointer, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	handle, err := syscall.CreateFile(
		targetPointer,
		syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		fileFlagOpenReparsePoint|fileFlagBackupSemantics,
		0,
	)
	if err != nil {
		return fmt.Errorf("open junction directory: %w", err)
	}
	defer syscall.CloseHandle(handle)

	substitute := `\??\` + strings.TrimSuffix(source, `\`)
	printName := strings.TrimSuffix(source, `\`)
	substituteUTF16 := utf16.Encode([]rune(substitute))
	printUTF16 := utf16.Encode([]rune(printName))
	pathUnits := append(append(append([]uint16{}, substituteUTF16...), 0), printUTF16...)
	pathUnits = append(pathUnits, 0)
	pathBytes := make([]byte, len(pathUnits)*2)
	for i, value := range pathUnits {
		binary.LittleEndian.PutUint16(pathBytes[i*2:], value)
	}
	dataLength := 8 + len(pathBytes)
	buffer := make([]byte, 8+dataLength)
	binary.LittleEndian.PutUint32(buffer[0:], ioReparseTagMountPoint)
	binary.LittleEndian.PutUint16(buffer[4:], uint16(dataLength))
	binary.LittleEndian.PutUint16(buffer[8:], 0)
	binary.LittleEndian.PutUint16(buffer[10:], uint16(len(substituteUTF16)*2))
	binary.LittleEndian.PutUint16(buffer[12:], uint16((len(substituteUTF16)+1)*2))
	binary.LittleEndian.PutUint16(buffer[14:], uint16(len(printUTF16)*2))
	copy(buffer[16:], pathBytes)
	var returned uint32
	if err := syscall.DeviceIoControl(
		handle,
		fsctlSetReparsePoint,
		&buffer[0],
		uint32(len(buffer)),
		nil,
		0,
		&returned,
		nil,
	); err != nil {
		return fmt.Errorf("create NTFS junction: %w", err)
	}
	success = true
	return nil
}
