//go:build windows

package sandbox

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
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
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if nonblocking {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &overlapped); err != nil {
		_ = f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, errSandboxLeaseBusy
		}
		return nil, err
	}
	return f, nil
}
