package opencode

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	a := New("", "")
	assert.Equal(t, "opencode", a.BinaryPath)
	assert.Equal(t, "", a.Model)

	a = New("/usr/local/bin/opencode", "anthropic/claude-sonnet-4")
	assert.Equal(t, "/usr/local/bin/opencode", a.BinaryPath)
	assert.Equal(t, "anthropic/claude-sonnet-4", a.Model)
}

func TestBuildRunArgs(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		message string
		want    []string
	}{
		{
			name:    "no model",
			model:   "",
			message: "do the thing",
			want:    []string{"run", "--dangerously-skip-permissions", "do the thing"},
		},
		{
			name:    "with model",
			model:   "anthropic/claude-sonnet-4",
			message: "do the thing",
			want:    []string{"run", "--dangerously-skip-permissions", "--model", "anthropic/claude-sonnet-4", "do the thing"},
		},
		{
			name:    "openai model",
			model:   "openai/gpt-5",
			message: "task body",
			want:    []string{"run", "--dangerously-skip-permissions", "--model", "openai/gpt-5", "task body"},
		},
		{
			name:    "empty message",
			model:   "",
			message: "",
			want:    []string{"run", "--dangerously-skip-permissions", ""},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildRunArgs(tc.model, tc.message)
			assert.Equal(t, tc.want, got)
			// Prompt is always the final positional argument.
			assert.Equal(t, tc.message, got[len(got)-1])
		})
	}
}

func TestBuildInteractiveArgs(t *testing.T) {
	tests := []struct {
		name           string
		model          string
		combinedPrompt string
		want           []string
	}{
		{
			name:           "no model",
			model:          "",
			combinedPrompt: "system\n\nuser",
			want:           []string{"--prompt", "system\n\nuser"},
		},
		{
			name:           "with model",
			model:          "anthropic/claude-sonnet-4",
			combinedPrompt: "system\n\nuser",
			want:           []string{"--model", "anthropic/claude-sonnet-4", "--prompt", "system\n\nuser"},
		},
		{
			name:           "empty prompt with model",
			model:          "openai/gpt-5",
			combinedPrompt: "",
			want:           []string{"--model", "openai/gpt-5", "--prompt", ""},
		},
		{
			name:           "no `run` subcommand",
			model:          "anthropic/claude-sonnet-4",
			combinedPrompt: "x",
			want:           []string{"--model", "anthropic/claude-sonnet-4", "--prompt", "x"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildInteractiveArgs(tc.model, tc.combinedPrompt)
			assert.Equal(t, tc.want, got)
			assert.NotContains(t, got, "run",
				"interactive args must NOT contain the `run` subcommand")
			assert.NotContains(t, got, "--dangerously-skip-permissions",
				"interactive args do not include --dangerously-skip-permissions")
		})
	}
}

// writeFakeOpenCode creates a shell script that records its argv to argvDump,
// optionally writes the supplied planJSON to planOutputPath (skipped when
// empty), then exits with exitCode.
//
// Argv elements are NUL-separated in the dump so that multi-line argument
// values (e.g. the combined system+initial prompt passed to --prompt) round-
// trip without being split on internal newlines.
func writeFakeOpenCode(t *testing.T, exitCode int, planOutputPath, planJSON string) (binary, argvDump string) {
	t.Helper()
	dir := t.TempDir()
	argvDump = filepath.Join(dir, "argv.bin")
	binary = filepath.Join(dir, "opencode.sh")
	extra := ""
	if planOutputPath != "" {
		extra = fmt.Sprintf("printf '%%s' '%s' > '%s'\n", planJSON, planOutputPath)
	}
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\0' \"$@\" > '%s'\n%sexit %d\n", argvDump, extra, exitCode)
	require.NoError(t, os.WriteFile(binary, []byte(script), 0o755))
	return binary, argvDump
}

func readArgvDump(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	raw := string(data)
	if raw == "" {
		return nil
	}
	// Each element is terminated by a NUL byte; trim the trailing NUL.
	raw = strings.TrimRight(raw, "\x00")
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\x00")
}
