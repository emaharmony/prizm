//go:build !windows

package tool

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// configureShellCommand places the shell and its descendants in a dedicated
// process group. CommandContext otherwise kills only the shell process, which
// can leave a child holding stdout or stderr open and cause Cmd.Wait to block
// until that child exits.
func configureShellCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 250 * time.Millisecond
}
