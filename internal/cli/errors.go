package cli

import (
	"errors"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/platform"
	"github.com/herd-os/herd/internal/platform/github"
)

// Exit codes per spec (docs/specs/03-cli/01-commands.md).
const (
	ExitSuccess     = 0
	ExitGeneral     = 1
	ExitInvalidArgs = 2
	ExitAPIError    = 3
	ExitConfigError = 4
)

// ConfigError wraps errors related to .herdos.yml.
type ConfigError struct {
	Err error
}

func (e *ConfigError) Error() string { return e.Err.Error() }
func (e *ConfigError) Unwrap() error { return e.Err }

// APIError wraps errors from GitHub API calls.
type APIError struct {
	Err error
}

func (e *APIError) Error() string { return e.Err.Error() }
func (e *APIError) Unwrap() error { return e.Err }

// loadConfigOrExit loads config and wraps the error as ConfigError.
func loadConfigOrExit() (*config.Config, error) {
	cfg, err := config.Load(".")
	if err != nil {
		return nil, &ConfigError{Err: err}
	}
	return cfg, nil
}

// newClientOrExit creates a GitHub client and wraps the error as APIError.
func newClientOrExit(owner, repo string) (platform.Platform, error) {
	client, err := github.New(owner, repo)
	if err != nil {
		return nil, &APIError{Err: err}
	}
	return client, nil
}

// ExitCode returns the appropriate exit code for an error.
func ExitCode(err error) int {
	if err == nil {
		return ExitSuccess
	}
	var ce *ConfigError
	if errors.As(err, &ce) {
		return ExitConfigError
	}
	var ae *APIError
	if errors.As(err, &ae) {
		return ExitAPIError
	}
	return ExitGeneral
}
