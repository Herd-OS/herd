package opencode

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

func TestPlan_ReadsPlanFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	planJSON := `{"batch_name":"fake-batch","tasks":[{"title":"x","description":"y","acceptance_criteria":["z"]}]}`

	tests := []struct {
		name      string
		exitCode  int
		writeFile bool
		wantErr   bool
	}{
		{name: "success path", exitCode: 0, writeFile: true, wantErr: false},
		{name: "agent failure", exitCode: 1, writeFile: false, wantErr: true},
		{name: "no plan file produced", exitCode: 0, writeFile: false, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repoRoot := t.TempDir()
			outputPath := filepath.Join(repoRoot, "plan.json")
			outPath := ""
			if tc.writeFile {
				outPath = outputPath
			}
			binary, argvDump := writeFakeOpenCode(t, tc.exitCode, outPath, planJSON)
			o := New(binary, "")

			plan, err := o.Plan(context.Background(), "tiny prompt", agent.PlanOptions{
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

			// Verify the rendered system prompt is NOT split out into a
			// --system-prompt-file flag — opencode has none.
			assert.NotContains(t, argv, "--system-prompt-file",
				"opencode has no --system-prompt-file flag")
			assert.NotContains(t, argv, "--system-prompt",
				"opencode has no --system-prompt flag")

			// --prompt is present and the prompt is its argument.
			var promptValue string
			for i, a := range argv {
				if a == "--prompt" {
					require.Less(t, i+1, len(argv), "expected a value after --prompt")
					promptValue = argv[i+1]
					break
				}
			}
			require.NotEmpty(t, promptValue, "argv must contain --prompt with a value")

			// The combined prompt should include both the rendered planning
			// system prompt and the initial prompt.
			assert.Contains(t, promptValue, "You are a planning assistant for HerdOS",
				"combined prompt must contain the rendered system prompt")
			assert.True(t, strings.HasSuffix(promptValue, "tiny prompt"),
				"combined prompt must end with the initial prompt")
		})
	}
}

func TestPlan_NoInitialPrompt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	repoRoot := t.TempDir()
	outputPath := filepath.Join(repoRoot, "plan.json")
	planJSON := `{"batch_name":"x","tasks":[{"title":"t","description":"d","acceptance_criteria":["a"]}]}`
	binary, argvDump := writeFakeOpenCode(t, 0, outputPath, planJSON)
	o := New(binary, "")

	plan, err := o.Plan(context.Background(), "", agent.PlanOptions{
		RepoRoot:   repoRoot,
		OutputPath: outputPath,
		Context:    map[string]string{},
	})
	require.NoError(t, err)
	require.NotNil(t, plan)

	argv := readArgvDump(t, argvDump)
	require.NotEmpty(t, argv)

	// When initialPrompt is empty, the combined prompt is just the
	// rendered system prompt (no trailing "\n\n" + "" concatenation).
	var promptValue string
	for i, a := range argv {
		if a == "--prompt" {
			promptValue = argv[i+1]
			break
		}
	}
	require.NotEmpty(t, promptValue)
	assert.Contains(t, promptValue, "You are a planning assistant for HerdOS")
	assert.False(t, strings.HasSuffix(promptValue, "\n\n"),
		"combined prompt must not have a dangling separator when initialPrompt is empty")
}

func TestPlan_PassesModel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	repoRoot := t.TempDir()
	outputPath := filepath.Join(repoRoot, "plan.json")
	planJSON := `{"batch_name":"x","tasks":[{"title":"t","description":"d","acceptance_criteria":["a"]}]}`
	binary, argvDump := writeFakeOpenCode(t, 0, outputPath, planJSON)
	o := New(binary, "anthropic/claude-sonnet-4")

	_, err := o.Plan(context.Background(), "go", agent.PlanOptions{
		RepoRoot:   repoRoot,
		OutputPath: outputPath,
		Context:    map[string]string{},
	})
	require.NoError(t, err)

	argv := readArgvDump(t, argvDump)
	require.NotEmpty(t, argv)
	assert.Contains(t, argv, "--model")
	assert.Contains(t, argv, "anthropic/claude-sonnet-4")
}

// TestPlan_RejectsOversizedPrompt verifies that Plan returns a clear,
// guarded error when the combined system+initial prompt exceeds the safe
// argv limit, rather than letting the kernel fail the exec with the opaque
// "argument list too long".
func TestPlan_RejectsOversizedPrompt(t *testing.T) {
	repoRoot := t.TempDir()
	outputPath := filepath.Join(repoRoot, "plan.json")
	huge := strings.Repeat("x", maxArgvPromptBytes+1)
	o := New("/bin/true", "")

	plan, err := o.Plan(context.Background(), huge, agent.PlanOptions{
		RepoRoot:   repoRoot,
		OutputPath: outputPath,
		Context:    map[string]string{},
	})
	require.Error(t, err)
	assert.Nil(t, plan)
	assert.Contains(t, err.Error(), "exceeds the safe argv limit",
		"expected a clear ARG_MAX-style error message")
}

func TestDiscuss_RequiresSystemPrompt(t *testing.T) {
	o := New("", "")
	err := o.Discuss(context.Background(), agent.DiscussOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "system prompt is required")
}

func TestDiscuss_PropagatesAgentExitError(t *testing.T) {
	o := New("/nonexistent/opencode-binary-xyz", "")
	err := o.Discuss(context.Background(), agent.DiscussOptions{
		SystemPrompt: "hello",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "opencode exited with error")
}

// TestDiscuss_RejectsOversizedPrompt verifies that Discuss returns a clear,
// guarded error when the combined system+initial prompt exceeds the safe
// argv limit, rather than letting the kernel fail the exec with the opaque
// "argument list too long".
func TestDiscuss_RejectsOversizedPrompt(t *testing.T) {
	o := New("/bin/true", "")
	huge := strings.Repeat("y", maxArgvPromptBytes+1)

	err := o.Discuss(context.Background(), agent.DiscussOptions{
		SystemPrompt: huge,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds the safe argv limit",
		"expected a clear ARG_MAX-style error message")
}

func TestDiscuss_FoldsSystemAndInitialIntoPrompt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	tests := []struct {
		name          string
		systemPrompt  string
		initialPrompt string
		wantContains  []string
		wantExact     string
	}{
		{
			name:          "system + initial",
			systemPrompt:  "you are a helper",
			initialPrompt: "kick off",
			wantContains:  []string{"you are a helper", "kick off"},
			wantExact:     "you are a helper\n\nkick off",
		},
		{
			name:          "system only",
			systemPrompt:  "you are a helper",
			initialPrompt: "",
			wantContains:  []string{"you are a helper"},
			wantExact:     "you are a helper",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			binary, argvDump := writeFakeOpenCode(t, 0, "", "")
			o := New(binary, "")

			err := o.Discuss(context.Background(), agent.DiscussOptions{
				SystemPrompt:  tc.systemPrompt,
				InitialPrompt: tc.initialPrompt,
			})
			require.NoError(t, err)

			argv := readArgvDump(t, argvDump)
			require.NotEmpty(t, argv)

			assert.NotContains(t, argv, "--system-prompt-file")
			assert.NotContains(t, argv, "--system-prompt")

			var promptValue string
			for i, a := range argv {
				if a == "--prompt" {
					promptValue = argv[i+1]
					break
				}
			}
			require.NotEmpty(t, promptValue)
			for _, want := range tc.wantContains {
				assert.Contains(t, promptValue, want)
			}
			assert.Equal(t, tc.wantExact, promptValue)
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
		run  func(context.Context, *OpenCodeAgent, string) error
	}{
		{
			name: "review",
			run: func(ctx context.Context, o *OpenCodeAgent, repoRoot string) error {
				_, err := o.Review(ctx, "diff body", agent.ReviewOptions{
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
			script := filepath.Join(dir, "opencode.sh")
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
