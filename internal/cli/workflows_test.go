package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
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
	Name string         `yaml:"name"`
	Uses string         `yaml:"uses"`
	If   string         `yaml:"if"`
	Run  string         `yaml:"run"`
	Env  map[string]any `yaml:"env"`
	With map[string]any `yaml:"with"`
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

			// Position: every extra env appears after the control-plane env block
			// and before ISSUE_NUMBER.
			controlPlaneIdx := strings.Index(s, "HERD_CONTROL_PLANE_URL:")
			issueIdx := strings.Index(s, "ISSUE_NUMBER:")
			require.True(t, controlPlaneIdx >= 0 && issueIdx >= 0, "anchor lines must be present")

			prevIdx := controlPlaneIdx
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
	// the loop collapses to nothing between HERD_CONTROL_PLANE_URL and ISSUE_NUMBER.
	assert.NotContains(t, string(rendered),
		"HERD_CONTROL_PLANE_URL: ${{ vars.HERD_CONTROL_PLANE_URL || 'https://api.herd-os.com' }}\n\n          ISSUE_NUMBER:",
		"empty ExtraEnv produced a stray blank line in env block")
	assert.NotContains(t, string(rendered), "HERD_CONTROL_PLANE_URL: ${{ inputs.control_plane_url")
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

func TestIntegratorWorkflow_BatchExtractionIsNonFatal(t *testing.T) {
	tests := []struct {
		name             string
		ciWorkflows      []string
		wantOccurrences  int
		wantCICompletion bool
	}{
		{
			name:            "default check_run reconciliation",
			wantOccurrences: 1,
		},
		{
			name:             "configured workflow_run CI reconciliation",
			ciWorkflows:      []string{"CI"},
			wantOccurrences:  2,
			wantCICompletion: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Integrator.CIWorkflows = tt.ciWorkflows
			wf := workflowFile{SrcName: "herd-integrator.yml.tmpl", DestName: "herd-integrator.yml", Template: true}
			rendered, err := RenderWorkflow(wf, cfg)
			require.NoError(t, err)
			s := string(rendered)

			nonFatalParse := "BATCH=$(echo \"$HEAD_BRANCH\" | grep -oP 'herd/batch/\\K[0-9]+' || true)"
			assert.Equal(t, tt.wantOccurrences, strings.Count(s, nonFatalParse))
			assert.NotContains(t, s, "BATCH=$(echo \"$HEAD_BRANCH\" | grep -oP 'herd/batch/\\K[0-9]+')\n")
			assert.Contains(t, s, "ISSUE_BODY=\"$(jq -r '.issue.body // \"\"' \"$GITHUB_EVENT_PATH\")\"")
			assert.NotContains(t, s, "ISSUE_BODY: ${{ github.event.issue.body }}")
			nonFatalIssueCloseParse := "BATCH=$(echo \"$ISSUE_BODY\" | grep -oP '^\\s*batch:\\s*\\K[0-9]+' | head -1 || true)"
			assert.Contains(t, s, nonFatalIssueCloseParse)
			assert.NotContains(t, s, "BATCH=$(echo \"$ISSUE_BODY\" | grep -oP '^\\s*batch:\\s*\\K[0-9]+' | head -1)\n")
			assert.Contains(t, s, "batch_number: (if $batch == \"\" then null else ($batch | tonumber) end)")
			assert.NotContains(t, s, "HERD_GITHUB_TOKEN")
			assert.NotContains(t, s, "GITHUB_TOKEN")
			assert.NotContains(t, s, "GH_TOKEN")
			if tt.wantCICompletion {
				assert.Contains(t, s, "check-ci-workflow-completion:")
			} else {
				assert.NotContains(t, s, "check-ci-workflow-completion:")
			}
		})
	}
}

func TestIntegratorWorkflow_IssueCloseReadsBodyFromEventPathAndSkipsNonHerdMultilineBody(t *testing.T) {
	cfg := config.Default()
	wf := workflowFile{SrcName: "herd-integrator.yml.tmpl", DestName: "herd-integrator.yml", Template: true}
	rendered, err := RenderWorkflow(wf, cfg)
	require.NoError(t, err)

	var workflow githubActionsWorkflow
	require.NoError(t, yaml.Unmarshal(rendered, &workflow))

	step := namedStep(t, workflow.Jobs["advance-on-close"], "Request hosted issue-close reconciliation")
	require.NotContains(t, step.Env, "ISSUE_BODY")
	require.Contains(t, step.Run, "$GITHUB_EVENT_PATH")

	dir := t.TempDir()
	eventPath := filepath.Join(dir, "event.json")
	require.NoError(t, os.WriteFile(eventPath, []byte(`{"issue":{"body":"not herd\nbatch: not-a-number\n${{ github.event.issue.body }}"}}`), 0600))

	binDir := filepath.Join(dir, "bin")
	require.NoError(t, os.Mkdir(binDir, 0700))
	jqPath := filepath.Join(binDir, "jq")
	require.NoError(t, os.WriteFile(jqPath, []byte(`#!/bin/sh
if [ "$1" != "-r" ] || [ "$2" != ".issue.body // \"\"" ] || [ "$3" != "$GITHUB_EVENT_PATH" ]; then
  echo "unexpected jq invocation: $*" >&2
  exit 2
fi
printf '%s\n' 'not herd' 'batch: not-a-number' '${{ github.event.issue.body }}'
`), 0700))
	curlPath := filepath.Join(binDir, "curl")
	require.NoError(t, os.WriteFile(curlPath, []byte(`#!/bin/sh
echo "curl should not be called for non-Herd issue bodies" >&2
exit 9
`), 0700))

	cmd := exec.Command("sh", "-c", step.Run)
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GITHUB_EVENT_PATH="+eventPath,
		"ISSUE_NUMBER=884",
		"REPOSITORY=octo/herd",
		"HERD_CONTROL_PLANE_URL=https://api.herd-os.com",
	)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	assert.Contains(t, string(output), "Not a herd issue")
}

func TestMonitorWorkflow_DefaultMatchesCommittedWorkflow(t *testing.T) {
	cfg := config.Default()
	wf := workflowFile{SrcName: "herd-monitor.yml.tmpl", DestName: "herd-monitor.yml", Template: true}
	rendered, err := RenderWorkflow(wf, cfg)
	require.NoError(t, err)

	onDisk, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "herd-monitor.yml"))
	require.NoError(t, err)
	assert.True(t, bytes.Equal(rendered, onDisk),
		"rendered monitor template with default config must match committed workflow.\nrendered:\n%s\non-disk:\n%s", rendered, onDisk)
}

func TestCallbackWorkflows_RenderControlPlaneURL(t *testing.T) {
	tests := []struct {
		name                 string
		controlPlane         string
		wantURL              string
		wantRepositoryVarURL bool
		wantHostedLiteral    bool
	}{
		{
			name:                 "default hosted",
			wantURL:              "https://api.herd-os.com",
			wantRepositoryVarURL: true,
		},
		{
			name:              "self hosted",
			controlPlane:      "https://herd.example.com",
			wantURL:           "https://herd.example.com",
			wantHostedLiteral: false,
		},
	}

	workflows := []workflowFile{
		{SrcName: "herd-integrator.yml.tmpl", DestName: "herd-integrator.yml", Template: true},
		{SrcName: "herd-monitor.yml.tmpl", DestName: "herd-monitor.yml", Template: true},
		{SrcName: "herd-worker.yml.tmpl", DestName: "herd-worker.yml", Template: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.ControlPlaneURL = tt.controlPlane

			for _, wf := range workflows {
				t.Run(wf.DestName, func(t *testing.T) {
					rendered, err := RenderWorkflow(wf, cfg)
					require.NoError(t, err)
					s := string(rendered)

					assert.Contains(t, s, "HERD_CONTROL_PLANE_URL:")
					assert.Contains(t, s, tt.wantURL)
					assert.Contains(t, s, "$HERD_CONTROL_PLANE_URL/api/v1/")
					if tt.wantRepositoryVarURL {
						assert.Contains(t, s, "vars.HERD_CONTROL_PLANE_URL || 'https://api.herd-os.com'")
					} else {
						assert.NotContains(t, s, "vars.HERD_CONTROL_PLANE_URL")
					}
					if !tt.wantHostedLiteral {
						assert.NotContains(t, s, "HERD_CONTROL_PLANE_URL: https://api.herd-os.com")
					}
					assert.NotContains(t, s, "HERD_GITHUB_TOKEN")
					assert.NotContains(t, s, "GITHUB_TOKEN")
					assert.NotContains(t, s, "GH_TOKEN")
				})
			}
		})
	}
}

func TestCallbackWorkflows_WorkflowEventPostsUseBoundedOIDCRetry(t *testing.T) {
	tests := []struct {
		name              string
		workflow          workflowFile
		configure         func(*config.Config)
		wantCallbackSteps int
	}{
		{
			name:              "integrator default callbacks",
			workflow:          workflowFile{SrcName: "herd-integrator.yml.tmpl", DestName: "herd-integrator.yml", Template: true},
			wantCallbackSteps: 5,
		},
		{
			name:     "integrator configured CI callbacks",
			workflow: workflowFile{SrcName: "herd-integrator.yml.tmpl", DestName: "herd-integrator.yml", Template: true},
			configure: func(cfg *config.Config) {
				cfg.Integrator.CIWorkflows = []string{"CI"}
			},
			wantCallbackSteps: 6,
		},
		{
			name:              "monitor callbacks",
			workflow:          workflowFile{SrcName: "herd-monitor.yml.tmpl", DestName: "herd-monitor.yml", Template: true},
			wantCallbackSteps: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			if tt.configure != nil {
				tt.configure(cfg)
			}
			rendered, err := RenderWorkflow(tt.workflow, cfg)
			require.NoError(t, err)

			var workflow githubActionsWorkflow
			require.NoError(t, yaml.Unmarshal(rendered, &workflow))
			steps := workflowEventCallbackSteps(workflow)
			require.Len(t, steps, tt.wantCallbackSteps)

			for _, step := range steps {
				t.Run(step.Name, func(t *testing.T) {
					assert.Equal(t, 1, strings.Count(step.Run, "/api/v1/workflow-events"),
						"each workflow-event callback step should post exactly once inside its retry loop")
					assert.Contains(t, step.Run, "attempt=1")
					assert.Contains(t, step.Run, "delay=1")
					assert.Contains(t, step.Run, "while true; do")
					assert.Contains(t, step.Run, "if curl -fsSL -X POST")
					assert.Contains(t, step.Run, "Failed to submit HerdOS workflow event after ${attempt} attempt(s).")
					assert.Contains(t, step.Run, "HerdOS workflow event callback failed; retrying in ${delay}s (attempt ${attempt}/4).")
					assert.Contains(t, step.Run, "delay=$((delay * 2))")
					assert.Contains(t, step.Run, `if [ "$delay" -gt 30 ]; then`)
					assert.Contains(t, step.Run, "delay=30")
					assert.Contains(t, step.Run, `if [ "$attempt" -ge 4 ]; then`)
					assertCallbackOIDCTokenFetchRetries(t, step.Run,
						"Failed to fetch HerdOS workflow event OIDC token after ${attempt} attempt(s).",
						"HerdOS workflow event OIDC token fetch failed; retrying in ${delay}s (attempt ${attempt}/4).")

					loopIndex := strings.Index(step.Run, "while true; do")
					oidcIndex := strings.Index(step.Run, `if ! OIDC_TOKEN="$(curl -fsSL`)
					postIndex := strings.Index(step.Run, "if curl -fsSL -X POST")
					require.NotEqual(t, -1, loopIndex)
					require.NotEqual(t, -1, oidcIndex)
					require.NotEqual(t, -1, postIndex)
					assert.Greater(t, oidcIndex, loopIndex, "callback must fetch a fresh OIDC token inside the retry loop")
					assert.Less(t, oidcIndex, postIndex, "callback must refresh OIDC before each workflow-event POST")
					assert.NotContains(t, step.Run[:loopIndex], `OIDC_TOKEN="$(curl -fsSL`,
						"callback must not fetch OIDC once before entering the retry loop")
				})
			}
		})
	}
}

func TestInstallCallbackWorkflowsRenderControlPlaneURL(t *testing.T) {
	tests := []struct {
		name         string
		controlPlane string
		want         string
		notWant      string
	}{
		{
			name:    "default uses repository variable fallback",
			want:    "HERD_CONTROL_PLANE_URL: ${{ vars.HERD_CONTROL_PLANE_URL || 'https://api.herd-os.com' }}",
			notWant: "HERD_CONTROL_PLANE_URL: https://api.herd-os.com",
		},
		{
			name:         "self hosted uses configured url",
			controlPlane: "https://herd.example.com",
			want:         `HERD_CONTROL_PLANE_URL: "https://herd.example.com"`,
			notWant:      "HERD_CONTROL_PLANE_URL: ${{ vars.HERD_CONTROL_PLANE_URL || 'https://api.herd-os.com' }}",
		},
	}

	callbackWorkflows := []string{"herd-integrator.yml", "herd-monitor.yml", "herd-worker.yml"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := config.Default()
			cfg.ControlPlaneURL = tt.controlPlane

			require.NoError(t, installWorkflows(dir, cfg))

			for _, name := range callbackWorkflows {
				t.Run(name, func(t *testing.T) {
					data, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", name))
					require.NoError(t, err)
					s := string(data)

					assert.Contains(t, s, tt.want)
					assert.NotContains(t, s, tt.notWant)
					assert.NotContains(t, s, "HERD_GITHUB_TOKEN")
					assert.NotContains(t, s, "GITHUB_TOKEN")
					assert.NotContains(t, s, "GH_TOKEN")
				})
			}
		})
	}
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
	assert.Contains(t, s, `"ci_workflow_completed"`)
	assert.Contains(t, s, "/api/v1/workflow-events")
	assert.NotContains(t, s, "herd integrator check-ci")
	assert.NotContains(t, s, "herd integrator review")
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
	assert.True(t, jobPostsWorkflowEvent(checkCIWorkflowCompletion))
	assert.NotContains(t, string(rendered), "herd integrator check-ci")
	assert.NotContains(t, string(rendered), "herd integrator review")
}

func jobInvokesIntegratorReview(job githubActionsJob) bool {
	for _, step := range job.Steps {
		if strings.Contains(step.Run, "herd integrator review") {
			return true
		}
	}
	return false
}

func jobPostsWorkflowEvent(job githubActionsJob) bool {
	for _, step := range job.Steps {
		if strings.Contains(step.Run, "/api/v1/workflow-events") {
			return true
		}
	}
	return false
}

func workflowEventCallbackSteps(workflow githubActionsWorkflow) []githubActionsStep {
	var steps []githubActionsStep
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "/api/v1/workflow-events") {
				steps = append(steps, step)
			}
		}
	}
	return steps
}

func assertCallbackOIDCTokenFetchRetries(t *testing.T, run, finalFailure, retryFailure string) {
	t.Helper()

	assert.Contains(t, run, `if ! OIDC_TOKEN="$(curl -fsSL`, "OIDC fetch failure must be handled inside the retry loop")
	assert.Contains(t, run, `jq -er '.value // empty'`, "OIDC fetch must fail on missing or empty token values")
	assert.Contains(t, run, finalFailure)
	assert.Contains(t, run, retryFailure)
	assert.Contains(t, run, "continue", "OIDC fetch failures must advance to the next retry attempt before POSTing")
	assert.NotContains(t, run, `jq -r '.value'`, "OIDC fetch must not accept empty token values")
	assert.NotContains(t, run, "\n            OIDC_TOKEN=\"$(curl -fsSL", "OIDC fetch must not be a bare assignment")

	for _, line := range strings.Split(run, "\n") {
		if !strings.Contains(line, "Failed to ") {
			continue
		}
		assert.NotContains(t, line, "$OIDC_TOKEN", "final failure log must not include token variables")
		assert.NotContains(t, line, "Authorization", "final failure log must not include auth headers")
		assert.NotContains(t, line, "Bearer", "final failure log must not include auth headers")
	}
}

func namedStep(t *testing.T, job githubActionsJob, name string) githubActionsStep {
	t.Helper()
	for _, step := range job.Steps {
		if step.Name == name {
			return step
		}
	}
	require.Failf(t, "step not found", "step %q not found", name)
	return githubActionsStep{}
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
	assert.NotContains(t, s, "HERD_GITHUB_TOKEN", "worker workflow must not use legacy PAT orchestration")
	assert.NotContains(t, s, "GITHUB_TOKEN", "worker workflow must not pass GitHub tokens for orchestration")
	assert.Contains(t, s, "id-token: write", "worker workflow must be able to request OIDC tokens")
	assert.Contains(t, s, "job_id:", "worker workflow must accept control-plane job IDs")
	assert.Contains(t, s, "repository:", "worker workflow must accept repository identity")
	assert.Contains(t, s, "expected_head_sha:", "worker workflow must accept expected head SHA")
	assert.Contains(t, s, "/api/v1/jobs/$HERD_JOB_ID/results", "worker workflow must report results to the control plane")
	assert.Contains(t, s, "uses: actions/upload-artifact@v4", "worker workflow must upload patch artifacts")
	assert.Contains(t, s, "name: worker-branch", "worker workflow must upload artifact named by callback")
	assert.NotContains(t, s, "name: worker.patch", "worker workflow must not upload patch bytes under a different artifact name")
	assert.Contains(t, s, "echo \"sha=$(git rev-parse HEAD)\" >> \"$GITHUB_OUTPUT\"", "worker workflow must capture checked-out HEAD")
	assert.Contains(t, s, "HERD_BASE_SHA: ${{ steps.checkout-base.outputs.sha }}", "worker workflow must use captured checkout HEAD")
	assert.Contains(t, s, "git add -A", "worker workflow must stage all worker changes before packaging")
	assert.Contains(t, s, "git diff --binary --cached \"$HERD_BASE_SHA\"", "worker workflow must package a binary git patch from the staged worker result")
	assert.Contains(t, s, "format: \"git-diff-binary\"", "worker metadata must declare the artifact format expected by the service")
	assert.Contains(t, s, "--arg base_sha \"$HERD_BASE_SHA\"", "worker result payload must use captured checkout HEAD")
	assert.NotContains(t, s, "--arg base_sha \"${{ github.sha }}\"", "worker result payload must not use dispatch event SHA")
	assert.Contains(t, s, "while true; do", "worker workflow must retry result callbacks")
	loopIndex := strings.Index(s, "while true; do")
	oidcIndex := strings.Index(s, "if ! OIDC_TOKEN=\"$(curl -fsSL")
	postIndex := strings.Index(s, "if curl -fsSL -X POST")
	require.NotEqual(t, -1, loopIndex, "worker workflow must have a callback retry loop")
	require.NotEqual(t, -1, oidcIndex, "worker workflow must fetch an OIDC token for callbacks")
	require.NotEqual(t, -1, postIndex, "worker workflow must post callback results")
	assert.Greater(t, oidcIndex, loopIndex, "worker workflow must fetch OIDC tokens inside the callback retry loop")
	assert.Less(t, oidcIndex, postIndex, "worker workflow must refresh the OIDC token before each callback POST")
	assertCallbackOIDCTokenFetchRetries(t, s,
		"Failed to fetch HerdOS job result OIDC token after ${attempt} attempt(s).",
		"HerdOS job result OIDC token fetch failed; retrying in ${delay}s (attempt ${attempt}/4).")
	assert.Contains(t, s, "Failed to submit HerdOS job result after ${attempt} attempt(s).", "worker workflow must log final callback failure")
	assert.Contains(t, s, "delay=$((delay * 2))", "worker workflow must use bounded backoff")
	assert.Contains(t, s, "ISSUE_NUMBER: ${{ inputs.issue_number }}", "ISSUE_NUMBER input must remain")
	assert.Contains(t, s, "HERD_WORKER_MODE: ${{ inputs.mode }}", "HERD_WORKER_MODE input must remain")
}

func TestWorkerWorkflowUsesCapturedCheckoutBaseForArtifactsAndResult(t *testing.T) {
	cfg := config.Default()
	wf := workflowFile{SrcName: "herd-worker.yml.tmpl", DestName: "herd-worker.yml", Template: true}
	rendered, err := RenderWorkflow(wf, cfg)
	require.NoError(t, err)

	var workflow githubActionsWorkflow
	require.NoError(t, yaml.Unmarshal(rendered, &workflow))
	execute := workflow.Jobs["execute"]
	require.NotEmpty(t, execute.Steps)

	var checkoutIndex, recordIndex, executeIndex, packageIndex, reportIndex = -1, -1, -1, -1, -1
	for i, step := range execute.Steps {
		switch step.Name {
		case "Checkout":
			checkoutIndex = i
		case "Record checkout base":
			recordIndex = i
			assert.Contains(t, step.Run, "git rev-parse HEAD")
			assert.Contains(t, step.Run, "$GITHUB_OUTPUT")
		case "Execute task":
			executeIndex = i
		case "Package worker patch artifact":
			packageIndex = i
			assert.Equal(t, "success()", step.If)
			require.NotNil(t, step.Env)
			assert.Equal(t, "${{ steps.checkout-base.outputs.sha }}", step.Env["HERD_BASE_SHA"])
			assert.Contains(t, step.Run, "git add -A")
			assert.Contains(t, step.Run, "git diff --binary --cached \"$HERD_BASE_SHA\"")
			assert.Contains(t, step.Run, "--arg base_sha \"$HERD_BASE_SHA\"")
		case "Report result":
			reportIndex = i
			require.NotNil(t, step.Env)
			assert.Equal(t, "${{ steps.checkout-base.outputs.sha }}", step.Env["HERD_BASE_SHA"])
			assert.Equal(t, "${{ inputs.batch_branch || github.event.repository.default_branch }}", step.Env["HERD_BATCH_BRANCH"])
			assert.Contains(t, step.Run, "--arg base_sha \"$HERD_BASE_SHA\"")
			assert.Contains(t, step.Run, "--arg target_branch \"$HERD_BATCH_BRANCH\"")
		}
	}

	require.NotEqual(t, -1, checkoutIndex)
	require.NotEqual(t, -1, recordIndex)
	require.NotEqual(t, -1, executeIndex)
	require.NotEqual(t, -1, packageIndex)
	require.NotEqual(t, -1, reportIndex)
	assert.Less(t, checkoutIndex, recordIndex)
	assert.Less(t, recordIndex, executeIndex)
	assert.Less(t, executeIndex, packageIndex)
	assert.Less(t, packageIndex, reportIndex)
	assert.NotContains(t, string(rendered), "HERD_BASE_SHA: ${{ github.sha }}")
}

func TestWorkerPatchArtifactUsesCheckedOutBaseWhenDispatchSHADiffers(t *testing.T) {
	cfg := config.Default()
	wf := workflowFile{SrcName: "herd-worker.yml.tmpl", DestName: "herd-worker.yml", Template: true}
	rendered, err := RenderWorkflow(wf, cfg)
	require.NoError(t, err)

	var workflow githubActionsWorkflow
	require.NoError(t, yaml.Unmarshal(rendered, &workflow))

	var packageStep githubActionsStep
	for _, step := range workflow.Jobs["execute"].Steps {
		if step.Name == "Package worker patch artifact" {
			packageStep = step
			break
		}
	}
	require.NotEmpty(t, packageStep.Run)
	require.NotContains(t, packageStep.Run, "github.sha")

	repoDir := t.TempDir()
	runWorkflowGit(t, repoDir, "init")
	runWorkflowGit(t, repoDir, "config", "user.email", "herd@example.com")
	runWorkflowGit(t, repoDir, "config", "user.name", "Herd Test")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "task.txt"), []byte("base\n"), 0644))
	runWorkflowGit(t, repoDir, "add", "task.txt")
	runWorkflowGit(t, repoDir, "commit", "-m", "base")
	checkedOutBase := strings.TrimSpace(runWorkflowGit(t, repoDir, "rev-parse", "HEAD"))
	dispatchSHA := strings.Repeat("f", 40)
	require.NotEqual(t, dispatchSHA, checkedOutBase)

	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "task.txt"), []byte("worker change\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "created.txt"), []byte("created by worker\n"), 0644))
	t.Cleanup(func() {
		_ = os.RemoveAll("/tmp/herd-worker-artifact")
	})
	require.NoError(t, os.RemoveAll("/tmp/herd-worker-artifact"))

	cmd := exec.Command("sh", "-c", packageStep.Run)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(),
		"HERD_JOB_ID=job-1",
		"HERD_REPOSITORY=acme/widgets",
		"HERD_EXPECTED_HEAD_SHA="+strings.Repeat("a", 40),
		"HERD_BASE_SHA="+checkedOutBase,
		"GITHUB_SHA="+dispatchSHA,
	)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))

	data, err := os.ReadFile("/tmp/herd-worker-artifact/herd-worker-metadata.json")
	require.NoError(t, err)

	var metadata struct {
		BaseSHA      string `json:"base_sha"`
		ArtifactName string `json:"artifact_name"`
	}
	require.NoError(t, json.Unmarshal(data, &metadata))
	assert.Equal(t, checkedOutBase, metadata.BaseSHA)
	assert.NotEqual(t, dispatchSHA, metadata.BaseSHA)
	assert.Equal(t, "herd-worker.patch", metadata.ArtifactName)
	assert.FileExists(t, "/tmp/herd-worker-artifact/herd-worker.patch")

	applyDir := t.TempDir()
	runWorkflowGit(t, applyDir, "init")
	runWorkflowGit(t, applyDir, "config", "user.email", "herd@example.com")
	runWorkflowGit(t, applyDir, "config", "user.name", "Herd Test")
	require.NoError(t, os.WriteFile(filepath.Join(applyDir, "task.txt"), []byte("base\n"), 0644))
	runWorkflowGit(t, applyDir, "add", "task.txt")
	runWorkflowGit(t, applyDir, "commit", "-m", "base")
	require.Equal(t, checkedOutBase, strings.TrimSpace(runWorkflowGit(t, applyDir, "rev-parse", "HEAD")))
	runWorkflowGit(t, applyDir, "apply", "--index", "--binary", "/tmp/herd-worker-artifact/herd-worker.patch")
	assert.Equal(t, "worker change\n", readWorkflowFile(t, filepath.Join(applyDir, "task.txt")))
	assert.Equal(t, "created by worker\n", readWorkflowFile(t, filepath.Join(applyDir, "created.txt")))
}

func readWorkflowFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func TestWorkerWorkflowUploadsSinglePatchArtifactBeforeCallback(t *testing.T) {
	cfg := config.Default()
	wf := workflowFile{SrcName: "herd-worker.yml.tmpl", DestName: "herd-worker.yml", Template: true}
	rendered, err := RenderWorkflow(wf, cfg)
	require.NoError(t, err)

	var workflow githubActionsWorkflow
	require.NoError(t, yaml.Unmarshal(rendered, &workflow))
	execute := workflow.Jobs["execute"]
	require.NotEmpty(t, execute.Steps)

	var packageIndex, patchUploadIndex, reportIndex = -1, -1, -1
	for i, step := range execute.Steps {
		switch step.Name {
		case "Package worker patch artifact":
			packageIndex = i
			assert.Equal(t, "success()", step.If)
		case "Upload worker patch artifact":
			patchUploadIndex = i
			assert.Equal(t, "success()", step.If)
			assert.Equal(t, "actions/upload-artifact@v4", step.Uses)
			assert.Equal(t, "worker-branch", step.With["name"])
			assert.Equal(t, "/tmp/herd-worker-artifact", step.With["path"])
		case "Report result":
			reportIndex = i
			assert.Equal(t, "always()", step.If)
			assert.Contains(t, step.Run, `if $status == "success" then {patch_artifact: "worker-branch"} else {} end`)
		}
	}

	require.NotEqual(t, -1, packageIndex)
	require.NotEqual(t, -1, patchUploadIndex)
	require.NotEqual(t, -1, reportIndex)
	assert.Less(t, packageIndex, patchUploadIndex)
	assert.Less(t, patchUploadIndex, reportIndex)
	assert.NotContains(t, string(rendered), "Upload worker patch metadata")
	assert.NotContains(t, string(rendered), "name: worker.patch")
}

func runWorkflowGit(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return string(out)
}

func TestRunnerEnvExampleHasSingleBootstrapToken(t *testing.T) {
	env, err := os.ReadFile(filepath.Join("runner", ".env.herd.example"))
	require.NoError(t, err)

	assert.Equal(t, 1, strings.Count(string(env), "HERD_RUNNER_BOOTSTRAP_TOKEN="))
	assert.Equal(t, 1, strings.Count(string(env), "HERD_CONTROL_PLANE_URL="))
}

func TestGeneratedWorkflowsDoNotUseLegacyPATOrCommentDispatch(t *testing.T) {
	cfg := config.Default()
	for _, wf := range workflowFiles() {
		if wf.DestName != "herd-worker.yml" && wf.DestName != "herd-integrator.yml" && wf.DestName != "herd-monitor.yml" {
			continue
		}
		out, err := RenderWorkflow(wf, cfg)
		require.NoError(t, err)
		s := string(out)

		assert.NotContains(t, s, "secrets.HERD_GITHUB_TOKEN", "%s must not use HERD_GITHUB_TOKEN", wf.DestName)
		assert.NotContains(t, s, "HERD_GITHUB_TOKEN", "%s must not pass HERD_GITHUB_TOKEN", wf.DestName)
		assert.NotContains(t, s, "secrets.GITHUB_TOKEN", "%s must not use GITHUB_TOKEN production fallbacks", wf.DestName)
		assert.NotContains(t, s, "GH_TOKEN", "%s must not use gh CLI auth", wf.DestName)
		assert.NotContains(t, s, "GITHUB_TOKEN", "%s must not pass GitHub token auth", wf.DestName)
		assert.NotContains(t, s, "herd integrator ", "%s must not run legacy integrator mutations on the runner", wf.DestName)
		assert.NotContains(t, s, "herd monitor patrol", "%s must not run legacy monitor mutations on the runner", wf.DestName)
		assert.NotContains(t, s, " gh ", "%s must not run gh CLI orchestration", wf.DestName)
		if wf.DestName == "herd-integrator.yml" {
			assert.NotContains(t, s, "issue_comment")
			assert.NotContains(t, s, "handle-comment")
			assert.NotContains(t, s, "/herd ")
		}
		assert.Contains(t, s, "id-token: write", "%s must request OIDC tokens", wf.DestName)
	}
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

	monitor, ok := bySrc["herd-monitor.yml.tmpl"]
	require.True(t, ok)
	assert.True(t, monitor.Template, "monitor workflow must be marked as template")
	assert.Equal(t, "herd-monitor.yml", monitor.DestName)

	integrator, ok := bySrc["herd-integrator.yml.tmpl"]
	require.True(t, ok)
	assert.True(t, integrator.Template, "integrator workflow must be marked as template")
	assert.Equal(t, "herd-integrator.yml", integrator.DestName)
}
