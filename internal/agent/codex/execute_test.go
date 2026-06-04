package codex

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/herd-os/herd/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecute_CommandArgs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	tests := []struct {
		name          string
		model         string
		effort        string
		body          string
		systemPrompt  string
		wantPrompt    string   // positional prompt expected in argv
		wantArgs      []string // flags expected in argv
		wantFlagVals  [][2]string
		wantNotInArgs []string
	}{
		{
			name:       "no model uses body, default medium effort",
			body:       "do the thing",
			wantPrompt: "do the thing",
			wantArgs: []string{
				"exec", "--sandbox", "workspace-write", "--skip-git-repo-check",
				"--ephemeral", "--ignore-user-config", "--output-last-message",
			},
			wantFlagVals:  [][2]string{{"-c", "model_reasoning_effort=medium"}},
			wantNotInArgs: []string{"--model", "--full-auto"},
		},
		{
			name:         "system prompt overrides body, model + high effort",
			model:        "gpt-5-codex",
			effort:       "high",
			body:         "task body",
			systemPrompt: "you are a worker",
			wantPrompt:   "you are a worker",
			wantArgs: []string{
				"exec", "--sandbox", "workspace-write", "--output-last-message",
			},
			wantFlagVals: [][2]string{
				{"--model", "gpt-5-codex"},
				{"-c", "model_reasoning_effort=high"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			binary, argvDump, _ := writeFakeCodex(t, "final agent message goes here", "", 0)

			a := NewAgent(binary, tc.model, tc.effort)
			task := agent.TaskSpec{Body: tc.body}
			opts := agent.ExecOptions{RepoRoot: t.TempDir(), SystemPrompt: tc.systemPrompt}

			result, err := a.Execute(context.Background(), task, opts)
			require.NoError(t, err)
			require.NotNil(t, result)
			// Final message is read from the --output-last-message file.
			assert.Equal(t, "final agent message goes here", result.Summary)

			argv := readArgvDump(t, argvDump)
			require.NotEmpty(t, argv)
			assert.Equal(t, "exec", argv[0], "exec subcommand must be first")

			for _, want := range tc.wantArgs {
				assert.Contains(t, argv, want, "argv missing %q", want)
			}
			for _, fv := range tc.wantFlagVals {
				assert.True(t, argvHasFlagValue(argv, fv[0], fv[1]),
					"argv must contain %q %q", fv[0], fv[1])
			}
			for _, notWant := range tc.wantNotInArgs {
				assert.NotContains(t, argv, notWant)
			}

			// --output-last-message must be followed by a non-empty path.
			var outPath string
			for i, v := range argv {
				if v == "--output-last-message" {
					require.Less(t, i+1, len(argv))
					outPath = argv[i+1]
				}
			}
			require.NotEmpty(t, outPath, "--output-last-message must carry a file path")

			// The prompt is the final positional argument.
			assert.Equal(t, tc.wantPrompt, argv[len(argv)-1],
				"prompt must be the last argv element")
		})
	}
}

func TestExecute_EnvMapsOpenAIKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	// Only OPENAI_API_KEY set; CODEX_API_KEY unset → child gets CODEX_API_KEY
	// populated from OPENAI_API_KEY.
	t.Setenv("CODEX_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "sk-openai-from-test")

	binary, _, envDump := writeFakeCodex(t, "the agent did the work successfully", "", 0)

	a := NewAgent(binary, "", "")
	_, err := a.Execute(context.Background(), agent.TaskSpec{Body: "do work"}, agent.ExecOptions{RepoRoot: t.TempDir()})
	require.NoError(t, err)

	env := readEnvDump(t, envDump)
	assert.Equal(t, "sk-openai-from-test", env["CODEX_API_KEY"],
		"CODEX_API_KEY must be populated from OPENAI_API_KEY in the child env")
}

func TestExecute_EnvPreservesExplicitCodexKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	// Both set; explicit CODEX_API_KEY must win.
	t.Setenv("CODEX_API_KEY", "sk-codex-explicit")
	t.Setenv("OPENAI_API_KEY", "sk-openai-from-test")

	binary, _, envDump := writeFakeCodex(t, "the agent did the work successfully", "", 0)

	a := NewAgent(binary, "", "")
	_, err := a.Execute(context.Background(), agent.TaskSpec{Body: "do work"}, agent.ExecOptions{RepoRoot: t.TempDir()})
	require.NoError(t, err)

	env := readEnvDump(t, envDump)
	assert.Equal(t, "sk-codex-explicit", env["CODEX_API_KEY"],
		"explicit CODEX_API_KEY must be preserved, not overwritten")
}

func TestExecute_RunsInRepoRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	repoRoot := t.TempDir()
	// Fake codex writes a marker file into its working directory and a
	// substantive summary into the --output-last-message file.
	binary := filepath.Join(t.TempDir(), "codex.sh")
	script := "#!/bin/sh\nout=''\nprev=''\nfor a in \"$@\"; do\n  if [ \"$prev\" = \"--output-last-message\" ]; then out=\"$a\"; fi\n  prev=\"$a\"\ndone\ntouch ran-here.marker\nprintf '%s' 'the agent completed the task in repo root' > \"$out\"\nexit 0\n"
	require.NoError(t, os.WriteFile(binary, []byte(script), 0o755))

	a := NewAgent(binary, "", "")
	result, err := a.Execute(context.Background(), agent.TaskSpec{Body: "do work"}, agent.ExecOptions{RepoRoot: repoRoot})
	require.NoError(t, err)
	assert.Contains(t, result.Summary, "completed the task")

	// The marker must have been created in repoRoot, proving cmd.Dir was set.
	_, statErr := os.Stat(filepath.Join(repoRoot, "ran-here.marker"))
	assert.NoError(t, statErr, "fake codex must run with cmd.Dir == opts.RepoRoot")
}

func TestExecute_FailingCommand(t *testing.T) {
	a := NewAgent("false", "", "") // "false" always exits 1
	_, err := a.Execute(context.Background(), agent.TaskSpec{Body: "test"}, agent.ExecOptions{RepoRoot: t.TempDir()})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "agent exited with error")
}

func TestExecute_MissingBinary(t *testing.T) {
	a := NewAgent("nonexistent-codex-binary-xyz", "", "")
	_, err := a.Execute(context.Background(), agent.TaskSpec{Body: "test"}, agent.ExecOptions{RepoRoot: t.TempDir()})
	assert.Error(t, err)
}

func TestExecute_SuspiciousOutputReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	// Final message is the suspicious "Execution error".
	binary, _, _ := writeFakeCodex(t, "Execution error", "", 0)
	a := NewAgent(binary, "", "")
	_, err := a.Execute(context.Background(), agent.TaskSpec{Body: "do work"}, agent.ExecOptions{RepoRoot: t.TempDir()})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "suspicious output after retry")
}

func TestExecute_EmptyFileFallsBackToStdout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	// No file content written; stdout carries the substantive message instead.
	binary, _, _ := writeFakeCodex(t, "", "this is a substantive multi word summary line", 0)
	a := NewAgent(binary, "", "")
	result, err := a.Execute(context.Background(), agent.TaskSpec{Body: "do work"}, agent.ExecOptions{RepoRoot: t.TempDir()})
	require.NoError(t, err)
	assert.Contains(t, result.Summary, "substantive multi word summary line")
}
