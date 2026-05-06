package claude

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscuss_RequiresSystemPrompt(t *testing.T) {
	c := New("", "")
	err := c.Discuss(context.Background(), agent.DiscussOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "system prompt is required")
}

func TestDiscuss_PropagatesAgentExitError(t *testing.T) {
	c := New("/nonexistent/claude-binary-xyz", "")
	err := c.Discuss(context.Background(), agent.DiscussOptions{
		SystemPrompt: "hello",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claude exited with error")
}

func TestDiscussOptions_DefaultsAreZero(t *testing.T) {
	opts := agent.DiscussOptions{}
	tests := []struct {
		name string
		got  string
	}{
		{"RepoRoot", opts.RepoRoot},
		{"SystemPrompt", opts.SystemPrompt},
		{"InitialPrompt", opts.InitialPrompt},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, "", tc.got)
		})
	}
}

// writeFakeClaude creates a shell script that records its argv to argvDump,
// optionally writes the supplied planJSON to planOutputPath (skipped when
// empty), then exits with exitCode. Used by tests that need to drive
// Discuss/Plan without invoking the real claude CLI.
func writeFakeClaude(t *testing.T, exitCode int, planOutputPath, planJSON string) (binary, argvDump string) {
	t.Helper()
	dir := t.TempDir()
	argvDump = filepath.Join(dir, "argv.txt")
	binary = filepath.Join(dir, "claude.sh")
	extra := ""
	if planOutputPath != "" {
		extra = fmt.Sprintf("printf '%%s' '%s' > '%s'\n", planJSON, planOutputPath)
	}
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > '%s'\n%sexit %d\n", argvDump, extra, exitCode)
	require.NoError(t, os.WriteFile(binary, []byte(script), 0o755))
	return binary, argvDump
}

func readArgvDump(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	raw := strings.TrimRight(string(data), "\n")
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}

func TestDiscuss_LargeSystemPrompt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	largePrompt := strings.Repeat("LARGE_PROMPT_BODY_X", 12*1024) // ~228KB

	tests := []struct {
		name     string
		exitCode int
		wantErr  bool
	}{
		{name: "success path", exitCode: 0, wantErr: false},
		{name: "non-zero exit cleans up", exitCode: 1, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			binary, argvDump := writeFakeClaude(t, tc.exitCode, "", "")
			c := New(binary, "")

			err := c.Discuss(context.Background(), agent.DiscussOptions{
				SystemPrompt:  largePrompt,
				InitialPrompt: "kick off",
			})
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "claude exited with error")
			} else {
				require.NoError(t, err)
			}

			argv := readArgvDump(t, argvDump)
			require.NotEmpty(t, argv, "fake binary should have recorded argv")

			joined := strings.Join(argv, "\n")
			assert.NotContains(t, joined, "LARGE_PROMPT_BODY_X",
				"the rendered system prompt must NOT appear on argv")

			var promptFile string
			for i, a := range argv {
				if a == "--system-prompt-file" {
					require.Less(t, i+1, len(argv), "expected a path after --system-prompt-file")
					promptFile = argv[i+1]
					break
				}
			}
			require.NotEmpty(t, promptFile, "argv must contain --system-prompt-file with a path")

			assert.Contains(t, argv, "--initial-prompt")
			assert.Contains(t, argv, "kick off")

			_, statErr := os.Stat(promptFile)
			assert.True(t, os.IsNotExist(statErr),
				"temp prompt file %s should be removed after Discuss returns (statErr=%v)",
				promptFile, statErr)
		})
	}
}

func TestDiscuss_TempFileCleanup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	binary, argvDump := writeFakeClaude(t, 1, "", "")
	c := New(binary, "")

	err := c.Discuss(context.Background(), agent.DiscussOptions{
		SystemPrompt: "small prompt",
	})
	require.Error(t, err)

	argv := readArgvDump(t, argvDump)
	require.NotEmpty(t, argv)

	var promptFile string
	for i, a := range argv {
		if a == "--system-prompt-file" {
			promptFile = argv[i+1]
			break
		}
	}
	require.NotEmpty(t, promptFile)

	_, statErr := os.Stat(promptFile)
	assert.True(t, os.IsNotExist(statErr),
		"temp prompt file should be cleaned up even on agent error")
}
