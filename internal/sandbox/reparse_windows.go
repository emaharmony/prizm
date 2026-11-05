//go:build windows

package sandbox

import (
	"os"
	"syscall"
)

func isReparsePoint(info os.FileInfo) bool {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&0x400 != 0 // FILE_ATTRIBUTE_REPARSE_POINT
}
