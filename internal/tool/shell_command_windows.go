//go:build windows

package tool

import (
	"os"
)

func shellCommand(command string) (string, []string) {
	shell := os.Getenv("ComSpec")
	if shell == "" {
		shell = "cmd.exe"
	}
	return shell, []string{"/d", "/s", "/c", command}
}
