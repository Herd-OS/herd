//go:build !windows

package process

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

func configureCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateCommand(cmd *exec.Cmd, waitCh <-chan error) {
	if cmd.Process == nil {
		return
	}

	// Setpgid makes the wrapper and its descendants share a private process
	// group, so signaling -pid reaches the whole tree.
	pgid := -cmd.Process.Pid
	if err := syscall.Kill(pgid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		_ = cmd.Process.Kill()
	}

	timer := time.NewTimer(defaultGracePeriod)
	defer timer.Stop()

	select {
	case <-waitCh:
		return
	case <-timer.C:
		_ = syscall.Kill(pgid, syscall.SIGKILL)
		_ = cmd.Process.Kill()
		<-waitCh
	}
}
