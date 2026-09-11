package platform

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const staleLockAge = 5 * time.Minute

type directoryLockOwner struct {
	PID       int       `json:"pid"`
	CreatedAt time.Time `json:"createdAt"`
	Token     string    `json:"token"`
}

func AcquireDirectoryLock(path string, timeout time.Duration) (func(), error) {
	deadline := time.Now().Add(timeout)
	for {
		err := os.Mkdir(path, 0o700)
		if err == nil {
			owner := directoryLockOwner{
				PID: os.Getpid(), CreatedAt: time.Now().UTC(), Token: lockToken(),
			}
			data, encodeErr := json.Marshal(owner)
			if encodeErr != nil {
				_ = os.Remove(path)
				return nil, fmt.Errorf("record transaction lock owner: %w", encodeErr)
			}
			if writeErr := os.WriteFile(filepath.Join(path, "owner.json"), data, 0o600); writeErr != nil {
				_ = os.RemoveAll(path)
				return nil, fmt.Errorf("record transaction lock owner: %w", writeErr)
			}
			return func() {
				current, readErr := readDirectoryLockOwner(path)
				if readErr == nil && current.Token == owner.Token {
					_ = os.RemoveAll(path)
				}
			}, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("acquire transaction lock: %w", err)
		}
		if removeStaleDirectoryLock(path) {
			continue
		}
		if time.Now().After(deadline) {
			if owner, ownerErr := readDirectoryLockOwner(path); ownerErr == nil {
				return nil, fmt.Errorf("timed out waiting for transaction lock held by process %d since %s",
					owner.PID, owner.CreatedAt.Format(time.RFC3339))
			}
			return nil, fmt.Errorf("timed out waiting for transaction lock")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func readDirectoryLockOwner(path string) (directoryLockOwner, error) {
	data, err := os.ReadFile(filepath.Join(path, "owner.json"))
	if err != nil {
		return directoryLockOwner{}, err
	}
	var owner directoryLockOwner
	if err := json.Unmarshal(data, &owner); err != nil {
		return directoryLockOwner{}, err
	}
	if owner.PID <= 0 || owner.Token == "" {
		return directoryLockOwner{}, fmt.Errorf("lock owner is incomplete")
	}
	return owner, nil
}

func removeStaleDirectoryLock(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return os.IsNotExist(err)
	}
	owner, ownerErr := readDirectoryLockOwner(path)
	stale := ownerErr == nil && !ProcessExists(owner.PID)
	if ownerErr != nil {
		stale = time.Since(info.ModTime()) > staleLockAge
	}
	if !stale {
		return false
	}
	tombstone := fmt.Sprintf("%s.stale-%d-%d", path, os.Getpid(), time.Now().UnixNano())
	if err := os.Rename(path, tombstone); err != nil {
		return os.IsNotExist(err)
	}
	_ = os.RemoveAll(tombstone)
	return true
}

func lockToken() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
}
