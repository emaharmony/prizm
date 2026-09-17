//go:build windows

package tool

import (
	"os/exec"
	"time"
)

// configureShellCommand bounds pipe cleanup after CommandContext terminates
// the shell. Windows does not expose Unix process groups through os/exec.
func configureShellCommand(cmd *exec.Cmd) {
	cmd.WaitDelay = 250 * time.Millisecond
}
