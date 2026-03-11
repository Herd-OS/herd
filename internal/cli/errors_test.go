package cli

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExitCode(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected int
	}{
		{"nil — success", nil, ExitSuccess},
		{"generic error", fmt.Errorf("something broke"), ExitGeneral},
		{"config error", &ConfigError{Err: fmt.Errorf("missing file")}, ExitConfigError},
		{"API error", &APIError{Err: fmt.Errorf("auth failed")}, ExitAPIError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, ExitCode(tt.err))
		})
	}
}

func TestExitCode_WrappedErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected int
	}{
		{
			"wrapped config error",
			fmt.Errorf("loading config: %w", &ConfigError{Err: fmt.Errorf("bad yaml")}),
			ExitConfigError,
		},
		{
			"wrapped API error",
			fmt.Errorf("github call: %w", &APIError{Err: fmt.Errorf("rate limit")}),
			ExitAPIError,
		},
		{
			"double-wrapped config error",
			fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", &ConfigError{Err: fmt.Errorf("deep")})),
			ExitConfigError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, ExitCode(tt.err))
		})
	}
}
