//go:build windows

package process

import "os/exec"

func configureCommand(cmd *exec.Cmd) {}

func terminateCommand(cmd *exec.Cmd, waitCh <-chan error) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	<-waitCh
}
