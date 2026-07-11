package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

var errGHNotFound = errors.New("gh CLI is not installed")

type ghRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

type ghAuthenticator struct {
	run ghRunner
}

func newGHAuthenticator() ghAuthenticator {
	return ghAuthenticator{run: runGHCommand}
}

func (a ghAuthenticator) SetupToken(ctx context.Context) (string, error) {
	if a.run == nil {
		a.run = runGHCommand
	}
	if _, err := a.run(ctx, "gh", "auth", "status", "-h", "github.com"); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("%w: install GitHub CLI and run `gh auth login -h github.com`", errGHNotFound)
		}
		return "", fmt.Errorf("GitHub CLI is not authenticated for github.com: run `gh auth login -h github.com` and ensure the active account can administer this repository: %w", err)
	}

	out, err := a.run(ctx, "gh", "auth", "token", "-h", "github.com")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("%w: install GitHub CLI and run `gh auth login -h github.com`", errGHNotFound)
		}
		return "", fmt.Errorf("could not read GitHub setup token from gh: run `gh auth login -h github.com`: %w", err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", fmt.Errorf("GitHub CLI returned an empty setup token: run `gh auth login -h github.com` and retry")
	}
	return token, nil
}

func runGHCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return out, nil
}
