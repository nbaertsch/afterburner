//go:build !windows

package launch

import "os"

func preserveInterruptInputBytes(_ *os.File) (func(), error) {
	return func() {}, nil
}
