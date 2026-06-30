//go:build windows

package process

import "os/exec"

func configureCommand(cmd *exec.Cmd, _ bool) {}

func terminateCommand(cmd *exec.Cmd, waitCh <-chan error, _ bool) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	<-waitCh
}
