//go:build !windows

package terminal

func modalClientMatchesAuthorizedPID(pid uint32, authorized map[uint32]struct{}) bool {
	_, ok := authorized[pid]
	return ok
}
