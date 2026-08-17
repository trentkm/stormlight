// Package filelock serializes access to atomically replaced state files.
package filelock

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

const retryInterval = 10 * time.Millisecond

// Acquire obtains an exclusive lock on path, waiting no longer than timeout.
func Acquire(path string, mode os.FileMode, timeout time.Duration) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, mode)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	deadline := time.Now().Add(timeout)
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				_ = file.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) &&
			!errors.Is(err, syscall.EAGAIN) &&
			!errors.Is(err, syscall.EINTR) {
			_ = file.Close()
			return nil, fmt.Errorf("acquire lock: %w", err)
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("acquire lock: timed out after %s", timeout)
		}
		time.Sleep(retryInterval)
	}
}
