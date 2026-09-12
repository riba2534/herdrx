//go:build unix

package herdr

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureOpenSSHProcess(cmd *exec.Cmd) {
	// Only the client and children we start (including ProxyCommand/ProxyJump)
	// enter this group. Existing SSH masters and remote Herdr processes do not.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
