package codex

import (
	"context"
	"runtime"
	"testing"

	"github.com/herd-os/herd/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReview_StructuredOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	tests := []struct {
		name         string
		output       string
		wantApproved bool
		wantFindings int
	}{
		{
			name:         "approved no findings",
			output:       `{"approved":true,"findings":[],"summary":"LGTM"}`,
			wantApproved: true,
			wantFindings: 0,
		},
		{
			name:         "rejected with findings",
			output:       `{"approved":false,"findings":[{"severity":"HIGH","description":"bug found here"}],"summary":"needs work"}`,
			wantApproved: false,
			wantFindings: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			binary, argvDump, _ := writeFakeCodex(t, tc.output, "", 0)

			a := NewAgent(binary, "", "", "")
			result, err := a.Review(context.Background(), "small diff", agent.ReviewOptions{
				AcceptanceCriteria: []string{"tests pass"},
				RepoRoot:           t.TempDir(),
			})
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tc.wantApproved, result.Approved)
			assert.Len(t, result.Findings, tc.wantFindings)
			// Comments must be backfilled from Findings.
			assert.Len(t, result.Comments, tc.wantFindings)
			if tc.wantFindings > 0 {
				assert.Equal(t, "HIGH", result.Findings[0].Severity)
				assert.Equal(t, "bug found here", result.Findings[0].Description)
				assert.Equal(t, "bug found here", result.Comments[0])
				assert.Equal(t, "needs work", result.Summary)
			}

			argv := readArgvDump(t, argvDump)
			require.NotEmpty(t, argv)
			assert.Equal(t, "exec", argv[0])

			// --output-schema must point at a materialized schema file.
			var schemaPath string
			for i, v := range argv {
				if v == "--output-schema" {
					require.Less(t, i+1, len(argv))
					schemaPath = argv[i+1]
				}
			}
			require.NotEmpty(t, schemaPath, "argv must contain --output-schema with a file path")
			assert.Contains(t, argv, "--output-last-message")
		})
	}
}

func TestReview_UnparseableOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	// Long, non-JSON output: passes the suspicious-output filter but fails to
	// parse as the review JSON contract.
	binary, _, _ := writeFakeCodex(t, "this output is definitely not valid json", "", 0)

	a := NewAgent(binary, "", "", "")
	result, err := a.Review(context.Background(), "small diff", agent.ReviewOptions{
		AcceptanceCriteria: []string{"tests pass"},
		RepoRoot:           t.TempDir(),
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Approved)
	assert.True(t, result.IsUnparseable)
	assert.Contains(t, result.Summary, "Failed to parse")
}

func TestReview_EnvMapsOpenAIKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("CODEX_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "sk-review-openai")

	binary, _, envDump := writeFakeCodex(t, `{"approved":true,"findings":[],"summary":"LGTM"}`, "", 0)

	a := NewAgent(binary, "", "", "")
	_, err := a.Review(context.Background(), "diff", agent.ReviewOptions{RepoRoot: t.TempDir()})
	require.NoError(t, err)

	env := readEnvDump(t, envDump)
	assert.Equal(t, "sk-review-openai", env["CODEX_API_KEY"],
		"Review must apply the OPENAI_API_KEY->CODEX_API_KEY mapping")
}

func TestReview_FailingCommand(t *testing.T) {
	a := NewAgent("false", "", "", "")
	_, err := a.Review(context.Background(), "diff", agent.ReviewOptions{RepoRoot: t.TempDir()})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "agent review exited with error")
}
