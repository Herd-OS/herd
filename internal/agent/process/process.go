package process

import (
	"context"
	"io"
	"os/exec"
	"time"
)

const defaultGracePeriod = 2 * time.Second

type Command struct {
	Path   string
	Args   []string
	Dir    string
	Env    []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// ProcessGroup starts the child in a private process group so context
	// cancellation can terminate descendants. Use it for headless commands.
	// Do not use it for interactive TUIs attached to the user's terminal:
	// moving the child out of the terminal foreground process group can stop
	// or hang the TUI when it reads from stdin.
	ProcessGroup bool
}

func Run(ctx context.Context, spec Command) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	cmd.Stdin = spec.Stdin
	cmd.Stdout = spec.Stdout
	cmd.Stderr = spec.Stderr
	configureCommand(cmd, spec.ProcessGroup)

	if err := cmd.Start(); err != nil {
		return err
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	select {
	case err := <-waitCh:
		return err
	case <-ctx.Done():
		terminateCommand(cmd, waitCh, spec.ProcessGroup)
		return ctx.Err()
	}
}
