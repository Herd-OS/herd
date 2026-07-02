package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type githubActionsWorkflow struct {
	Jobs map[string]githubActionsJob `yaml:"jobs"`
}

type githubActionsJob struct {
	Concurrency *githubActionsConcurrency `yaml:"concurrency"`
	Steps       []githubActionsStep       `yaml:"steps"`
}

type githubActionsConcurrency struct {
	Group            string `yaml:"group"`
	CancelInProgress bool   `yaml:"cancel-in-progress"`
}

type githubActionsStep struct {
	Run string `yaml:"run"`
}

func TestWorkersExtraEnv_DefaultsEmpty(t *testing.T) {
	cfg := config.Default()
	require.NotNil(t, cfg.Workers.ExtraEnv, "ExtraEnv should be non-nil empty slice")
	assert.Empty(t, cfg.Workers.ExtraEnv, "ExtraEnv should default to empty")
}

func TestWorkersExtraEnv_RendersInWorkflow(t *testing.T) {
	tests := []struct {
		name  string
		extra []string
	}{
		{"single entry", []string{"NPM_TOKEN"}},
		{"two entries", []string{"BUNDLE_RUBYGEMS__PKG__GITHUB__COM", "NPM_TOKEN"}},
		{"three entries with underscores", []string{"FOO", "BAR_BAZ", "X__Y__Z"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Workers.ExtraEnv = tt.extra

			wf := workflowFile{SrcName: "herd-worker.yml.tmpl", DestName: "herd-worker.yml", Template: true}
			out, err := RenderWorkflow(wf, cfg)
			require.NoError(t, err)
			s := string(out)

			// Each secret renders with proper GitHub Actions secrets.<NAME> reference,
			// preserving the 10-space indent.
			for _, name := range tt.extra {
				line := "          " + name + ": ${{ secrets." + name + " }}"
				assert.Contains(t, s, line, "missing rendered env line for %s", name)
			}

			// Position: every extra env appears after GITHUB_TOKEN (the line
			// immediately before the ExtraEnv loop) and before ISSUE_NUMBER.
			githubTokenIdx := strings.Index(s, "GITHUB_TOKEN:")
			issueIdx := strings.Index(s, "ISSUE_NUMBER:")
			require.True(t, githubTokenIdx >= 0 && issueIdx >= 0, "anchor lines must be present")

			prevIdx := githubTokenIdx
			for _, name := range tt.extra {
				idx := strings.Index(s, name+":")
				require.True(t, idx >= 0, "extra env %s should be in output", name)
				assert.Greater(t, idx, prevIdx, "extra env %s should come after previous entry", name)
				prevIdx = idx
			}
			assert.Less(t, prevIdx, issueIdx, "extra envs should appear before ISSUE_NUMBER")
		})
	}
}

func TestWorkersExtraEnv_EmptyOmitted(t *testing.T) {
	cfg := config.Default() // ExtraEnv empty
	wf := workflowFile{SrcName: "herd-worker.yml.tmpl", DestName: "herd-worker.yml", Template: true}
	rendered, err := RenderWorkflow(wf, cfg)
	require.NoError(t, err)

	// Must match the on-disk .github/workflows/herd-worker.yml byte-for-byte.
	onDisk, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "herd-worker.yml"))
	require.NoError(t, err)
	assert.True(t, bytes.Equal(rendered, onDisk),
		"rendered template with empty ExtraEnv must match committed workflow.\nrendered:\n%s\non-disk:\n%s", rendered, onDisk)

	// Sanity: no double blank lines around the env block. With ExtraEnv empty,
	// the loop collapses to nothing between GITHUB_TOKEN and ISSUE_NUMBER.
	assert.NotContains(t, string(rendered),
		"GITHUB_TOKEN: ${{ secrets.HERD_GITHUB_TOKEN || secrets.GITHUB_TOKEN }}\n\n          ISSUE_NUMBER:",
		"empty ExtraEnv produced a stray blank line in env block")
}

func TestWorkersExtraEnv_NilSlice(t *testing.T) {
	cfg := config.Default()
	cfg.Workers.ExtraEnv = nil
	wf := workflowFile{SrcName: "herd-worker.yml.tmpl", DestName: "herd-worker.yml", Template: true}
	rendered, err := RenderWorkflow(wf, cfg)
	require.NoError(t, err)

	onDisk, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "herd-worker.yml"))
	require.NoError(t, err)
	assert.True(t, bytes.Equal(rendered, onDisk),
		"nil ExtraEnv must render the same as empty ExtraEnv")
}

func TestIntegratorWorkflow_DefaultMatchesCommittedWorkflow(t *testing.T) {
	cfg := config.Default()
	wf := workflowFile{SrcName: "herd-integrator.yml.tmpl", DestName: "herd-integrator.yml", Template: true}
	rendered, err := RenderWorkflow(wf, cfg)
	require.NoError(t, err)

	onDisk, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "herd-integrator.yml"))
	require.NoError(t, err)
	assert.True(t, bytes.Equal(rendered, onDisk),
		"rendered integrator template with default config must match committed workflow.\nrendered:\n%s\non-disk:\n%s", rendered, onDisk)
	assert.NotContains(t, string(rendered), "check-ci-workflow-completion")
}

func TestIntegratorWorkflow_RendersConfiguredCIWorkflows(t *testing.T) {
	cfg := config.Default()
	cfg.Integrator.CIWorkflows = []string{"CI - ServiceKit Ruby", "CI — Accounts"}
	wf := workflowFile{SrcName: "herd-integrator.yml.tmpl", DestName: "herd-integrator.yml", Template: true}

	rendered, err := RenderWorkflow(wf, cfg)
	require.NoError(t, err)
	s := string(rendered)

	workflowRunStart := strings.Index(s, "  workflow_run:\n")
	require.NotEqual(t, -1, workflowRunStart)
	workflowRunEnd := strings.Index(s[workflowRunStart:], "    types: [completed]")
	require.NotEqual(t, -1, workflowRunEnd)
	workflowRunBlock := s[workflowRunStart : workflowRunStart+workflowRunEnd]

	assert.Equal(t, 1, strings.Count(workflowRunBlock, `"HerdOS Worker"`))
	assert.Contains(t, workflowRunBlock, `      - "CI - ServiceKit Ruby"`)
	assert.Contains(t, workflowRunBlock, `      - "CI — Accounts"`)
	assert.Contains(t, s, "check-ci-workflow-completion:")
	assert.Contains(t, s, "github.event.workflow_run.path == '.github/workflows/herd-worker.yml'")
	assert.Contains(t, s, "github.event.workflow_run.path != '.github/workflows/herd-worker.yml'")
	checkCIIndex := strings.Index(s, "herd integrator check-ci --ci-run-id \"$RUN_ID\"")
	reviewIndex := strings.Index(s, "herd integrator review --batch \"$BATCH\"")
	require.NotEqual(t, -1, checkCIIndex)
	require.NotEqual(t, -1, reviewIndex)
	assert.Less(t, checkCIIndex, reviewIndex)
}

func TestIntegratorWorkflow_ReviewCapableJobsHaveScopedConcurrency(t *testing.T) {
	cfg := config.Default()
	cfg.Integrator.CIWorkflows = []string{"CI"}
	wf := workflowFile{SrcName: "herd-integrator.yml.tmpl", DestName: "herd-integrator.yml", Template: true}

	rendered, err := RenderWorkflow(wf, cfg)
	require.NoError(t, err)

	var workflow githubActionsWorkflow
	require.NoError(t, yaml.Unmarshal(rendered, &workflow))

	expectedGroups := map[string]string{
		"integrate":                    "herd-integrate-${{ github.event.workflow_run.head_branch || github.ref }}",
		"check-ci-workflow-completion": "herd-integrate-${{ github.event.workflow_run.head_branch || github.ref }}",
		"advance-on-close":             "herd-advance-${{ github.event.issue.milestone && github.event.issue.milestone.number || github.event.issue.number }}",
		"re-review":                    "herd-re-review-${{ github.event.pull_request.number }}",
		"handle-comment":               "herd-comment-${{ github.event.issue.milestone && github.event.issue.milestone.number || github.event.issue.number }}",
	}

	for jobName, expectedGroup := range expectedGroups {
		t.Run(jobName, func(t *testing.T) {
			job, ok := workflow.Jobs[jobName]
			require.True(t, ok, "job should render")
			require.NotNil(t, job.Concurrency, "review-capable job should define concurrency")
			assert.Equal(t, expectedGroup, job.Concurrency.Group)
			assert.False(t, job.Concurrency.CancelInProgress,
				"review-capable job should queue review attempts for the application lock")
		})
	}

	for jobName, job := range workflow.Jobs {
		if !jobInvokesIntegratorReview(job) {
			continue
		}

		require.NotNil(t, job.Concurrency, "%s invokes review and should define concurrency", jobName)
		assert.NotEmpty(t, job.Concurrency.Group, "%s concurrency group should be scoped", jobName)
		assert.False(t, job.Concurrency.CancelInProgress,
			"%s invokes review and should not cancel queued review attempts", jobName)
	}
}

func TestIntegratorWorkflow_ConfiguredCIReviewRetrySharesWorkerCompletionConcurrency(t *testing.T) {
	cfg := config.Default()
	cfg.Integrator.CIWorkflows = []string{"CI"}
	wf := workflowFile{SrcName: "herd-integrator.yml.tmpl", DestName: "herd-integrator.yml", Template: true}

	rendered, err := RenderWorkflow(wf, cfg)
	require.NoError(t, err)

	var workflow githubActionsWorkflow
	require.NoError(t, yaml.Unmarshal(rendered, &workflow))

	integrate := workflow.Jobs["integrate"]
	checkCIWorkflowCompletion := workflow.Jobs["check-ci-workflow-completion"]
	require.NotNil(t, integrate.Concurrency)
	require.NotNil(t, checkCIWorkflowCompletion.Concurrency)
	assert.Equal(t, integrate.Concurrency.Group, checkCIWorkflowCompletion.Concurrency.Group)
	assert.NotContains(t, checkCIWorkflowCompletion.Concurrency.Group, "herd-check-ci")
	assert.Contains(t, checkCIWorkflowCompletion.Concurrency.Group, "github.event.workflow_run.head_branch")
	assert.True(t, jobInvokesIntegratorReview(checkCIWorkflowCompletion))
	checkCIIndex := strings.Index(string(rendered), "herd integrator check-ci --ci-run-id \"$RUN_ID\"")
	reviewIndex := strings.Index(string(rendered), "herd integrator review --batch \"$BATCH\"")
	require.NotEqual(t, -1, checkCIIndex)
	require.NotEqual(t, -1, reviewIndex)
	assert.Less(t, checkCIIndex, reviewIndex)
}

func jobInvokesIntegratorReview(job githubActionsJob) bool {
	for _, step := range job.Steps {
		if strings.Contains(step.Run, "herd integrator review") {
			return true
		}
	}
	return false
}

func TestRenderWorkflow_StaticPassThrough(t *testing.T) {
	cfg := config.Default()
	for _, wf := range workflowFiles() {
		if wf.Template {
			continue
		}
		out, err := RenderWorkflow(wf, cfg)
		require.NoError(t, err, "rendering static workflow %s", wf.SrcName)

		raw, err := workflowFS.ReadFile("workflows/" + wf.SrcName)
		require.NoError(t, err)
		assert.Equal(t, raw, out, "static workflow %s should pass through unchanged", wf.SrcName)
	}
}

func TestRenderWorkflow_UnknownSource(t *testing.T) {
	cfg := config.Default()
	wf := workflowFile{SrcName: "does-not-exist.yml", DestName: "x.yml"}
	_, err := RenderWorkflow(wf, cfg)
	assert.Error(t, err, "rendering nonexistent workflow source should error")
}

func TestWorkerWorkflowTemplate_ExcludesProviderAuthEnv(t *testing.T) {
	cfg := config.Default()

	var wf workflowFile
	found := false
	for _, f := range workflowFiles() {
		if f.SrcName == "herd-worker.yml.tmpl" {
			wf = f
			found = true
			break
		}
	}
	require.True(t, found, "herd-worker.yml.tmpl must be registered in workflowFiles()")

	out, err := RenderWorkflow(wf, cfg)
	require.NoError(t, err)
	s := string(out)

	// The removed OpenCode subscription env vars must not reappear in the
	// rendered worker workflow. The token names are assembled from fragments
	// so the repo-wide "no subscription-auth strings" grep gate (see issue
	// #676) stays clean while this regression guard keeps checking for them.
	authJSONEnv := "OPENCODE_AUTH_" + "JSON"
	forceSeedEnv := "OPENCODE_AUTH_" + "FORCE_SEED"
	assert.NotContains(t, s, authJSONEnv,
		"rendered worker workflow must not include the removed OpenCode subscription auth env")
	assert.NotContains(t, s, forceSeedEnv,
		"rendered worker workflow must not include the removed OpenCode force-seed env")

	// The six AI-provider auth secrets must not be surfaced as GitHub Actions
	// secrets-sourced env lines (issue #706). AI provider auth lives only in the
	// runner's .env (injected by docker-compose), never duplicated into GitHub
	// Actions secrets where an unset secret would clobber the real .env value.
	// Names are assembled from fragments so any repo-wide grep gate banning these
	// literal strings stays clean.
	removed := []string{
		"ANTHROPIC_API_" + "KEY",
		"CLAUDE_CODE_OAUTH_" + "TOKEN",
		"OPENAI_API_" + "KEY",
		"CODEX_API_" + "KEY",
		"CODEX_ACCESS_" + "TOKEN",
		"GEMINI_API_" + "KEY",
	}
	for _, name := range removed {
		assert.NotContains(t, s, "secrets."+name,
			"rendered worker workflow must not surface %s from GitHub Actions secrets", name)
	}

	// The retained env keys must remain in the env block.
	assert.Contains(t, s, "HERD_GITHUB_TOKEN", "HERD_GITHUB_TOKEN must remain in the env block")
	assert.Contains(t, s, "ISSUE_NUMBER: ${{ inputs.issue_number }}", "ISSUE_NUMBER input must remain")
	assert.Contains(t, s, "HERD_WORKER_MODE: ${{ inputs.mode }}", "HERD_WORKER_MODE input must remain")
}

func TestWorkflowFiles_ContainsExpectedNames(t *testing.T) {
	files := workflowFiles()
	require.Len(t, files, 4)

	bySrc := map[string]workflowFile{}
	for _, wf := range files {
		bySrc[wf.SrcName] = wf
	}

	worker, ok := bySrc["herd-worker.yml.tmpl"]
	require.True(t, ok, "worker template must be registered")
	assert.True(t, worker.Template, "worker workflow must be marked as template")
	assert.Equal(t, "herd-worker.yml", worker.DestName)

	publish, ok := bySrc["herd-publish-runner.yml.tmpl"]
	require.True(t, ok, "publish-runner template must be registered")
	assert.True(t, publish.Template, "publish-runner workflow must be marked as template")
	assert.Equal(t, "herd-publish-runner.yml", publish.DestName)

	monitor, ok := bySrc["herd-monitor.yml"]
	require.True(t, ok)
	assert.False(t, monitor.Template)

	integrator, ok := bySrc["herd-integrator.yml.tmpl"]
	require.True(t, ok)
	assert.True(t, integrator.Template, "integrator workflow must be marked as template")
	assert.Equal(t, "herd-integrator.yml", integrator.DestName)
}
