//go:build !windows

package platform

type ProcessInfo struct {
	PID  int
	Name string
	Path string
}

func RunningExecutables(names []string, excludePID int) ([]ProcessInfo, error) {
	return nil, nil
}

func FormatProcessList(processes []ProcessInfo) string {
	return ""
}
