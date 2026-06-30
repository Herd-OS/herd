//go:build !windows

package process

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

func configureCommand(cmd *exec.Cmd, processGroup bool) {
	if processGroup {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
}

func terminateCommand(cmd *exec.Cmd, waitCh <-chan error, processGroup bool) {
	if cmd.Process == nil {
		return
	}

	if !processGroup {
		_ = cmd.Process.Kill()
		<-waitCh
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
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	waitDone := false
	for {
		if waitDone && processGroupExited(pgid) {
			return
		}

		select {
		case <-waitCh:
			waitDone = true
		case <-ticker.C:
			if processGroupExited(pgid) {
				return
			}
		case <-timer.C:
			_ = syscall.Kill(pgid, syscall.SIGKILL)
			_ = cmd.Process.Kill()
			if !waitDone {
				<-waitCh
			}
			return
		}
	}
}

func processGroupExited(pgid int) bool {
	err := syscall.Kill(pgid, 0)
	return errors.Is(err, syscall.ESRCH)
}
