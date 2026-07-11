package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

var (
	errGHMissing         = errors.New("gh CLI is not installed")
	errGHUnauthenticated = errors.New("gh CLI is not authenticated for github.com")
	errGHEmptyToken      = errors.New("gh auth token returned an empty token")
)

type ghCommandRunner interface {
	LookPath(file string) (string, error)
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execGHCommandRunner struct{}

func (execGHCommandRunner) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

func (execGHCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

type ghAuthorizer struct {
	runner ghCommandRunner
}

func newGHAuthorizer(runner ghCommandRunner) ghAuthorizer {
	if runner == nil {
		runner = execGHCommandRunner{}
	}
	return ghAuthorizer{runner: runner}
}

func (a ghAuthorizer) SetupToken(ctx context.Context) (string, error) {
	if _, err := a.runner.LookPath("gh"); err != nil {
		return "", fmt.Errorf("%w: install GitHub CLI and run `gh auth login -h github.com`", errGHMissing)
	}
	statusOut, err := a.runner.Run(ctx, "gh", "auth", "status", "-h", "github.com")
	if err != nil {
		msg := strings.TrimSpace(string(statusOut))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%w: run `gh auth login -h github.com` with an account that has admin access to this repository (%s)", errGHUnauthenticated, msg)
	}
	if !bytes.Contains(bytes.ToLower(statusOut), []byte("github.com")) {
		return "", fmt.Errorf("%w: run `gh auth login -h github.com`; current gh auth status did not report github.com", errGHUnauthenticated)
	}

	tokenOut, err := a.runner.Run(ctx, "gh", "auth", "token", "-h", "github.com")
	if err != nil {
		msg := strings.TrimSpace(string(tokenOut))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("get setup token from gh: run `gh auth login -h github.com` and retry (%s)", msg)
	}
	token := strings.TrimSpace(string(tokenOut))
	if token == "" {
		return "", fmt.Errorf("%w: run `gh auth login -h github.com` and retry", errGHEmptyToken)
	}
	return token, nil
}
