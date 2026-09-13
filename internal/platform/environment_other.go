//go:build !windows

package platform

func LookupUserEnvironmentVariable(string) (string, bool, error) {
	return "", false, nil
}
