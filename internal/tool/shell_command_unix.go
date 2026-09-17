//go:build !windows

package tool

func shellCommand(command string) (string, []string) {
	return "/bin/sh", []string{"-c", command}
}
