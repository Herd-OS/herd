package codex

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/herd-os/herd/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fakePlanJSON = `{"batch_name":"codex-batch","tasks":[{"title":"do a thing","description":"desc","implementation_details":"impl","acceptance_criteria":["it works"],"scope":["a.go"],"conventions":[],"context_from_dependencies":[],"complexity":"low","type":"feature","runner_label":"","depends_on":[],"manual":false}]}`

func TestPlan_StructuredOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	tests := []struct {
		name     string
		model    string
		exitCode int
		wantErr  bool
	}{
		{name: "success path", model: "", exitCode: 0, wantErr: false},
		{name: "success with model", model: "gpt-5-codex", exitCode: 0, wantErr: false},
		{name: "agent failure", model: "", exitCode: 1, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repoRoot := t.TempDir()
			outputPath := filepath.Join(repoRoot, "plan.json")

			content := ""
			if tc.exitCode == 0 {
				content = fakePlanJSON
			}
			binary, argvDump, _ := writeFakeCodex(t, content, "", tc.exitCode)

			a := NewAgent(binary, tc.model, "")
			plan, err := a.Plan(context.Background(), "plan a feature", agent.PlanOptions{
				RepoRoot:   repoRoot,
				OutputPath: outputPath,
				Context:    map[string]string{},
			})

			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, plan)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, plan)
			assert.Equal(t, "codex-batch", plan.BatchName)
			require.NotEmpty(t, plan.Tasks)
			assert.Equal(t, "do a thing", plan.Tasks[0].Title)

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

			// --output-last-message must carry the requested output path.
			assert.True(t, argvHasFlagValue(argv, "--output-last-message", outputPath),
				"argv must contain --output-last-message %q", outputPath)

			if tc.model != "" {
				assert.True(t, argvHasFlagValue(argv, "--model", tc.model))
			}
		})
	}
}

func TestPlan_UsesTempFileWhenNoOutputPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	binary, argvDump, _ := writeFakeCodex(t, fakePlanJSON, "", 0)

	a := NewAgent(binary, "", "")
	// A non-empty initialPrompt keeps Plan on the headless path, which is where
	// the temp-file allocation lives.
	plan, err := a.Plan(context.Background(), "plan a feature", agent.PlanOptions{
		RepoRoot: t.TempDir(),
		Context:  map[string]string{},
	})
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.Equal(t, "codex-batch", plan.BatchName)

	argv := readArgvDump(t, argvDump)
	// A temp output path must still be passed via --output-last-message.
	var outPath string
	for i, v := range argv {
		if v == "--output-last-message" {
			require.Less(t, i+1, len(argv))
			outPath = argv[i+1]
		}
	}
	assert.NotEmpty(t, outPath, "Plan must allocate a temp output file when OutputPath is empty")
}

func TestPlan_EmptyInitialPromptUsesInteractive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	repoRoot := t.TempDir()
	outputPath := filepath.Join(repoRoot, "plan.json")
	binary, argvDump, envDump := writeFakeInteractiveCodex(t, fakePlanJSON, 0)

	a := NewAgent(binary, "", "")
	plan, err := a.Plan(context.Background(), "", agent.PlanOptions{
		RepoRoot:   repoRoot,
		OutputPath: outputPath,
		Context:    map[string]string{},
	})
	require.NoError(t, err)
	require.NotNil(t, plan)
	assert.Equal(t, "codex-batch", plan.BatchName)

	argv := readArgvDump(t, argvDump)
	// Interactive mode must NOT use the exec subcommand or headless
	// structured-output flags.
	require.NotEmpty(t, argv)
	assert.NotEqual(t, "exec", argv[0], "interactive plan must not invoke `codex exec`")
	assert.NotContains(t, argv, "--output-schema")
	assert.NotContains(t, argv, "--output-last-message")

	// The output path must be handed to the child via HERD_PLAN_OUT.
	env := readEnvDump(t, envDump)
	assert.Equal(t, outputPath, env["HERD_PLAN_OUT"])
	assert.NotEmpty(t, env["HERD_PLAN_SCHEMA"], "interactive plan must pass schema path via env")
}

func TestPlan_NonEmptyInitialPromptStaysHeadless(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake binary not supported on Windows")
	}

	repoRoot := t.TempDir()
	outputPath := filepath.Join(repoRoot, "plan.json")
	binary, argvDump, _ := writeFakeCodex(t, fakePlanJSON, "", 0)

	a := NewAgent(binary, "", "")
	plan, err := a.Plan(context.Background(), "add a feature", agent.PlanOptions{
		RepoRoot:   repoRoot,
		OutputPath: outputPath,
		Context:    map[string]string{},
	})
	require.NoError(t, err)
	require.NotNil(t, plan)

	argv := readArgvDump(t, argvDump)
	require.NotEmpty(t, argv)
	assert.Equal(t, "exec", argv[0], "non-empty initialPrompt must use headless `codex exec`")
	assert.True(t, argvHasFlagValue(argv, "--output-last-message", outputPath))
	assert.Contains(t, argv, "--output-schema")
}
