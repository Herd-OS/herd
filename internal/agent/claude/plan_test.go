package claude

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlan_LargeSystemPrompt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	repoRoot := t.TempDir()
	outputPath := filepath.Join(repoRoot, "plan.json")

	planJSON := `{"batch_name":"fake-batch","tasks":[{"title":"x","description":"y","acceptance_criteria":["z"]}]}`

	tests := []struct {
		name     string
		exitCode int
		wantErr  bool
	}{
		{name: "success path", exitCode: 0, wantErr: false},
		{name: "agent failure cleans up", exitCode: 1, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Each subcase needs its own binary + argv dump. Reuse the helper.
			outPath := outputPath
			if tc.exitCode != 0 {
				// Failure path doesn't need to write the plan file.
				outPath = ""
			}
			binary, argvDump := writeFakeClaude(t, tc.exitCode, outPath, planJSON)
			c := New(binary, "")

			plan, err := c.Plan(context.Background(), "tiny prompt", agent.PlanOptions{
				RepoRoot:   repoRoot,
				OutputPath: outputPath,
				Context:    map[string]string{},
			})
			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, plan)
			} else {
				require.NoError(t, err)
				require.NotNil(t, plan)
				assert.Equal(t, "fake-batch", plan.BatchName)
			}

			argv := readArgvDump(t, argvDump)
			require.NotEmpty(t, argv, "fake binary should have recorded argv")

			// The rendered system prompt always contains this template phrase.
			joined := strings.Join(argv, "\n")
			assert.NotContains(t, joined, "You are a planning assistant for HerdOS",
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

			assert.NotContains(t, argv, "--initial-prompt",
				"prompt must be passed as a positional, not via --initial-prompt flag")
			assert.Contains(t, argv, "tiny prompt")
			assert.Equal(t, "tiny prompt", argv[len(argv)-1],
				"initial prompt must be the last argv element when set")

			_, statErr := os.Stat(promptFile)
			assert.True(t, os.IsNotExist(statErr),
				"temp prompt file %s should be removed after Plan returns (statErr=%v)",
				promptFile, statErr)
		})
	}
}

func TestPlan_PassesInitialPromptAsPositional(t *testing.T) {
	const promptFile = "/tmp/system-prompt-xyz.txt"

	tests := []struct {
		name          string
		model         string
		initialPrompt string
	}{
		{name: "prompt set, model set", model: "sonnet-4", initialPrompt: "plan a feature"},
		{name: "prompt set, model empty", model: "", initialPrompt: "plan a feature"},
		{name: "prompt empty, model set", model: "sonnet-4", initialPrompt: ""},
		{name: "prompt empty, model empty", model: "", initialPrompt: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := New("/fake/claude", tc.model)
			argv := buildPlanArgs(c, tc.initialPrompt, promptFile)

			assert.NotContains(t, argv, "--initial-prompt",
				"argv must never contain the --initial-prompt flag")

			if tc.initialPrompt != "" {
				assert.Contains(t, argv, tc.initialPrompt,
					"initial prompt value must appear in argv as a positional")
				assert.Equal(t, tc.initialPrompt, argv[len(argv)-1],
					"initial prompt must be the last argv element")
			} else {
				assert.NotContains(t, argv, "",
					"empty initial prompt must not be appended as a positional")
			}

			assert.Contains(t, argv, "--system-prompt-file")
			assert.Contains(t, argv, promptFile)

			if tc.model != "" {
				assert.Contains(t, argv, "--model")
				assert.Contains(t, argv, tc.model)
			} else {
				assert.NotContains(t, argv, "--model")
			}
		})
	}
}

func TestReview_UnixContextCancellationTerminatesDescendants(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process group termination is Unix-only")
	}

	// Interactive plan/discuss sessions intentionally do not opt into process
	// groups because TUIs must remain in the terminal foreground process group;
	// internal/agent/process covers that default behavior directly. Headless
	// review/execute paths retain descendant cleanup.
	tests := []struct {
		name string
		run  func(context.Context, *ClaudeAgent, string) error
	}{
		{
			name: "review",
			run: func(ctx context.Context, c *ClaudeAgent, repoRoot string) error {
				_, err := c.Review(ctx, "diff body", agent.ReviewOptions{
					AcceptanceCriteria: []string{"works"},
					RepoRoot:           repoRoot,
				})
				return err
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			pidFile := filepath.Join(dir, "child.pid")
			readyFile := filepath.Join(dir, "ready")
			script := filepath.Join(dir, "claude.sh")
			content := fmt.Sprintf(`#!/bin/sh
cat > /dev/null
(sleep 60) &
child=$!
printf '%%s' "$child" > %s
touch %s
wait "$child"
`, shellQuote(pidFile), shellQuote(readyFile))
			require.NoError(t, os.WriteFile(script, []byte(content), 0o755))

			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			err := tc.run(ctx, New(script, ""), dir)

			require.ErrorIs(t, err, context.DeadlineExceeded)
			assert.FileExists(t, readyFile)
			pid := readPIDFile(t, pidFile)
			require.Eventually(t, func() bool {
				return !processAlive(pid)
			}, 3*time.Second, 25*time.Millisecond)
		})
	}
}
