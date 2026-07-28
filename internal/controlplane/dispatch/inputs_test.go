package dispatch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowInputs(t *testing.T) {
	tests := []struct {
		name string
		req  DispatchRequest
		want map[string]string
	}{
		{
			name: "worker",
			req: DispatchRequest{
				Owner:           "octo",
				Repo:            "herd",
				BatchNumber:     12,
				IssueNumber:     34,
				BatchBranch:     "herd/batch/12",
				BaseSHA:         "abc123",
				HeadSHA:         "abc123",
				ExpectedHeadSHA: "abc123",
				RunnerLabel:     "herd-worker",
				TimeoutMinutes:  45,
			},
			want: map[string]string{
				"repository_owner":  "octo",
				"repository_name":   "herd",
				"repository":        "octo/herd",
				"job_id":            "job-1",
				"batch_number":      "12",
				"issue_number":      "34",
				"batch_branch":      "herd/batch/12",
				"base_sha":          "abc123",
				"head_sha":          "abc123",
				"expected_head_sha": "abc123",
				"runner_label":      "herd-worker",
				"timeout_minutes":   "45",
			},
		},
		{
			name: "review callback identity",
			req: DispatchRequest{
				Owner:           "octo",
				Repo:            "herd",
				BatchNumber:     12,
				PRNumber:        8,
				BatchBranch:     "herd/batch/12",
				HeadSHA:         "def456",
				ExpectedHeadSHA: "def456",
				ControlPlaneURL: "https://cp.example.com",
				Reason:          "requested",
				ReviewPrompt:    "focus on auth and retries",
			},
			want: map[string]string{
				"repository_owner":  "octo",
				"repository_name":   "herd",
				"repository":        "octo/herd",
				"job_id":            "job-1",
				"batch_number":      "12",
				"pr_number":         "8",
				"batch_branch":      "herd/batch/12",
				"base_sha":          "def456",
				"head_sha":          "def456",
				"expected_head_sha": "def456",
				"reason":            "requested",
				"review_prompt":     "focus on auth and retries",
			},
		},
		{
			name: "monitor integrator callback identity",
			req: DispatchRequest{
				Owner:        "octo",
				Repo:         "herd",
				BatchNumber:  12,
				BatchBranch:  "herd/batch/12",
				WorkflowFile: "herd-monitor.yml",
			},
			want: map[string]string{
				"repository_owner":  "octo",
				"repository_name":   "herd",
				"repository":        "octo/herd",
				"job_id":            "job-1",
				"batch_number":      "12",
				"batch_branch":      "herd/batch/12",
				"base_sha":          "",
				"head_sha":          "",
				"expected_head_sha": "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := WorkflowInputs(tt.req, "job-1")

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestWorkflowInputsReviewPromptIsOptional(t *testing.T) {
	tests := []struct {
		name       string
		prompt     string
		wantPrompt bool
	}{
		{name: "empty prompt omitted"},
		{name: "non-empty prompt included", prompt: "focus on auth and retries", wantPrompt: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validRequest()
			req.Kind = JobKindReview
			req.PRNumber = 8
			req.IssueNumber = 0
			req.ReviewPrompt = tt.prompt

			got, err := WorkflowInputs(req, "job-1")

			require.NoError(t, err)
			if tt.wantPrompt {
				assert.Equal(t, tt.prompt, got["review_prompt"])
			} else {
				assert.NotContains(t, got, "review_prompt")
			}
		})
	}
}

func TestWorkflowInputsManualReviewIsOptional(t *testing.T) {
	tests := []struct {
		name       string
		kind       JobKind
		manual     bool
		wantManual bool
	}{
		{name: "review default omitted", kind: JobKindReview},
		{name: "review manual included", kind: JobKindReview, manual: true, wantManual: true},
		{name: "non-review manual omitted", kind: JobKindWorker, manual: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validRequest()
			req.Kind = tt.kind
			req.ManualReview = tt.manual
			if tt.kind == JobKindReview {
				req.PRNumber = 8
				req.IssueNumber = 0
			}

			got, err := WorkflowInputs(req, "job-1")

			require.NoError(t, err)
			if tt.wantManual {
				assert.Equal(t, "true", got["manual_review"])
			} else {
				assert.NotContains(t, got, "manual_review")
			}
		})
	}
}

func TestWorkflowInputsValidation(t *testing.T) {
	tests := []struct {
		name    string
		req     DispatchRequest
		jobID   string
		wantErr string
	}{
		{name: "missing job", req: validRequest(), wantErr: "job ID"},
		{name: "missing owner", req: func() DispatchRequest { r := validRequest(); r.Owner = ""; return r }(), jobID: "job-1", wantErr: "owner"},
		{name: "missing repo", req: func() DispatchRequest { r := validRequest(); r.Repo = ""; return r }(), jobID: "job-1", wantErr: "name"},
		{name: "missing batch", req: func() DispatchRequest { r := validRequest(); r.BatchNumber = 0; return r }(), jobID: "job-1", wantErr: "batch"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := WorkflowInputs(tt.req, tt.jobID)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
