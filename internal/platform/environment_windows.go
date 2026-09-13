//go:build windows

package platform

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

func LookupUserEnvironmentVariable(name string) (string, bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE)
	if err != nil {
		return "", false, err
	}
	defer key.Close()

	value, _, err := key.GetStringValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}
