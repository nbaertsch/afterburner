//go:build windows

package terminal

func modalClientMatchesAuthorizedPID(pid uint32, authorized map[uint32]struct{}) bool {
	if pid == 0 || len(authorized) == 0 {
		return false
	}
	_, ok := authorized[pid]
	return ok
}
