//go:build !windows

package sandbox

import (
	"errors"
	"os"
	"syscall"
)

func lockSandboxLease(path string, nonblocking bool) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	return lockSandboxFile(f, nonblocking)
}

func lockExistingSandboxLease(path string, nonblocking bool) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	return lockSandboxFile(f, nonblocking)
}

func lockSandboxFile(f *os.File, nonblocking bool) (*os.File, error) {
	flags := syscall.LOCK_EX
	if nonblocking {
		flags |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), flags); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errSandboxLeaseBusy
		}
		return nil, err
	}
	return f, nil
}
