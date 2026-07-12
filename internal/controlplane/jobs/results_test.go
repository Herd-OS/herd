package jobs

import (
	"encoding/json"
	"testing"

	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseResultPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    Result
		wantErr string
	}{
		{
			name: "valid worker completed",
			payload: `{
				"version":1,
				"kind":"worker_completed",
				"repository":"acme/widgets",
				"job_id":"job-1",
				"batch_number":106,
				"issue_number":837,
				"target_branch":"herd/worker/837",
				"base_sha":"base",
				"expected_head_sha":"head",
				"patch_artifact":"patches/job-1.diff",
				"status":"success"
			}`,
			want: WorkerCompletedResult{
				Version:         1,
				Kind:            KindWorkerCompleted,
				Repository:      "acme/widgets",
				JobID:           "job-1",
				BatchNumber:     106,
				IssueNumber:     837,
				TargetBranch:    "herd/worker/837",
				BaseSHA:         "base",
				ExpectedHeadSHA: "head",
				PatchArtifact:   "patches/job-1.diff",
				Status:          StatusSuccess,
			},
		},
		{
			name: "valid review completed",
			payload: `{
				"version":1,
				"kind":"review_completed",
				"repository":"acme/widgets",
				"job_id":"job-2",
				"batch_number":106,
				"pr_number":12,
				"head_sha":"head",
				"status":"approved",
				"summary":"looks good"
			}`,
			want: ReviewCompletedResult{
				Version:     1,
				Kind:        KindReviewCompleted,
				Repository:  "acme/widgets",
				JobID:       "job-2",
				BatchNumber: 106,
				PRNumber:    12,
				HeadSHA:     "head",
				Status:      StatusApproved,
				Summary:     "looks good",
			},
		},
		{
			name: "valid worker failure without patch artifact",
			payload: `{
				"version":1,
				"kind":"worker_completed",
				"repository":"acme/widgets",
				"job_id":"job-1",
				"batch_number":106,
				"issue_number":837,
				"target_branch":"herd/worker/837",
				"base_sha":"base",
				"expected_head_sha":"head",
				"status":"failure"
			}`,
			want: WorkerCompletedResult{
				Version:         1,
				Kind:            KindWorkerCompleted,
				Repository:      "acme/widgets",
				JobID:           "job-1",
				BatchNumber:     106,
				IssueNumber:     837,
				TargetBranch:    "herd/worker/837",
				BaseSHA:         "base",
				ExpectedHeadSHA: "head",
				Status:          StatusFailure,
			},
		},
		{
			name: "unknown version",
			payload: `{
				"version":2,
				"kind":"worker_completed",
				"repository":"acme/widgets",
				"job_id":"job-1"
			}`,
			wantErr: "unsupported result version",
		},
		{
			name: "unknown kind",
			payload: `{
				"version":1,
				"kind":"other_completed",
				"repository":"acme/widgets",
				"job_id":"job-1"
			}`,
			wantErr: "unsupported result kind",
		},
		{
			name: "missing repository",
			payload: `{
				"version":1,
				"kind":"worker_completed",
				"job_id":"job-1"
			}`,
			wantErr: "repository is required",
		},
		{
			name: "missing job ID",
			payload: `{
				"version":1,
				"kind":"worker_completed",
				"repository":"acme/widgets"
			}`,
			wantErr: "job_id is required",
		},
		{
			name: "invalid status",
			payload: `{
				"version":1,
				"kind":"worker_completed",
				"repository":"acme/widgets",
				"job_id":"job-1",
				"batch_number":106,
				"issue_number":837,
				"target_branch":"herd/worker/837",
				"base_sha":"base",
				"expected_head_sha":"head",
				"patch_artifact":"patches/job-1.diff",
				"status":"approved"
			}`,
			wantErr: "invalid worker result status",
		},
		{
			name: "success missing patch artifact",
			payload: `{
				"version":1,
				"kind":"worker_completed",
				"repository":"acme/widgets",
				"job_id":"job-1",
				"batch_number":106,
				"issue_number":837,
				"target_branch":"herd/worker/837",
				"base_sha":"base",
				"expected_head_sha":"head",
				"status":"success"
			}`,
			wantErr: "patch_artifact is required",
		},
		{
			name:    "malformed JSON",
			payload: `{"version":1`,
			wantErr: "malformed JSON result payload",
		},
		{
			name: "unknown field",
			payload: `{
				"version":1,
				"kind":"review_completed",
				"repository":"acme/widgets",
				"job_id":"job-2",
				"batch_number":106,
				"pr_number":12,
				"head_sha":"head",
				"status":"approved",
				"summary":"looks good",
				"extra":"nope"
			}`,
			wantErr: "unknown field",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseResultPayload([]byte(tt.payload))

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateResultAgainstJobRejectsStaleHeadSHA(t *testing.T) {
	result := ReviewCompletedResult{
		Version:     1,
		Kind:        KindReviewCompleted,
		Repository:  "acme/widgets",
		JobID:       "job-1",
		BatchNumber: 106,
		PRNumber:    12,
		HeadSHA:     "old",
		Status:      StatusApproved,
		Summary:     "looks good",
	}

	err := validateResultAgainstJob(result, store.Job{JobID: "job-1", HeadSHA: "new"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "stale head SHA")
}

func TestResultIdempotencyKeyUsesStableResultIdentity(t *testing.T) {
	payload := []byte(`{"version":1,"kind":"review_completed","repository":"acme/widgets","job_id":"job-1","batch_number":1,"pr_number":2,"head_sha":"head","status":"approved","summary":"ok"}`)
	result, err := ParseResultPayload(payload)
	require.NoError(t, err)
	reordered := []byte(`{
		"summary":"ok",
		"status":"approved",
		"head_sha":"head",
		"pr_number":2,
		"batch_number":1,
		"job_id":"job-1",
		"repository":"acme/widgets",
		"kind":"review_completed",
		"version":1
	}`)
	reorderedResult, err := ParseResultPayload(reordered)
	require.NoError(t, err)

	key := ResultIdempotencyKey(result, payload)

	assert.Contains(t, key, KindReviewCompleted+":")
	assert.Equal(t, key, ResultIdempotencyKey(reorderedResult, reordered))
	assert.NotEqual(t, ResultPayloadHash(payload), ResultPayloadHash(reordered))
	assert.Len(t, ResultPayloadHash(payload), 64)
}

func TestParseReviewCompletedTerminalFailureStatuses(t *testing.T) {
	tests := []string{StatusFailure, StatusTimedOut, StatusUnparseable, StatusMaxCyclesHit}
	for _, status := range tests {
		t.Run(status, func(t *testing.T) {
			payload := []byte(`{"version":1,"kind":"review_completed","repository":"acme/widgets","job_id":"job-1","batch_number":1,"pr_number":2,"head_sha":"head","status":"` + status + `","summary":"not approved"}`)

			result, err := ParseResultPayload(payload)

			require.NoError(t, err)
			assert.Equal(t, status, result.StatusValue())
		})
	}
}

func TestResultMetadataEmbedsPayload(t *testing.T) {
	payload := []byte(`{"version":1,"kind":"review_completed","repository":"acme/widgets","job_id":"job-1","batch_number":1,"pr_number":2,"head_sha":"head","status":"approved","summary":"ok"}`)

	metadata, err := resultMetadata(payload, OIDCClaims{Repository: "acme/widgets"}, nil)

	require.NoError(t, err)
	var decoded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(metadata, &decoded))
	assert.JSONEq(t, string(payload), string(decoded["payload"]))
}
