package github

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowServiceGetWorkflow(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/actions/workflows/herd-worker.yml", func(w http.ResponseWriter, r *http.Request) {
		resp := gh.Workflow{ID: gh.Ptr(int64(12345))}
		json.NewEncoder(w).Encode(resp)
	})

	client, _ := newTestClient(t, mux)
	id, err := client.Workflows().GetWorkflow(context.Background(), "herd-worker.yml")

	require.NoError(t, err)
	assert.Equal(t, int64(12345), id)
}

func TestWorkflowServiceDispatch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/test-org/test-repo/actions/workflows/herd-worker.yml/dispatches", func(w http.ResponseWriter, r *http.Request) {
		var event gh.CreateWorkflowDispatchEventRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&event))
		assert.Equal(t, "main", event.Ref)
		assert.Equal(t, "42", event.Inputs["issue_number"])

		w.WriteHeader(http.StatusNoContent)
	})

	client, _ := newTestClient(t, mux)
	run, err := client.Workflows().Dispatch(context.Background(), "herd-worker.yml", "main", map[string]string{
		"issue_number": "42",
	})

	require.NoError(t, err)
	assert.Nil(t, run) // fire-and-forget
}

func TestWorkflowServiceGetRun(t *testing.T) {
	ts := gh.Timestamp{Time: time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/actions/runs/99", func(w http.ResponseWriter, r *http.Request) {
		resp := gh.WorkflowRun{
			ID:         gh.Ptr(int64(99)),
			Status:     gh.Ptr("completed"),
			Conclusion: gh.Ptr("success"),
			HTMLURL:    gh.Ptr("https://github.com/test-org/test-repo/actions/runs/99"),
			CreatedAt:  &ts,
		}
		json.NewEncoder(w).Encode(resp)
	})

	client, _ := newTestClient(t, mux)
	run, err := client.Workflows().GetRun(context.Background(), 99)

	require.NoError(t, err)
	assert.Equal(t, int64(99), run.ID)
	assert.Equal(t, "completed", run.Status)
	assert.Equal(t, "success", run.Conclusion)
	assert.Equal(t, time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC), run.CreatedAt)
}

func TestWorkflowServiceListRuns(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "in_progress", r.URL.Query().Get("status"))
		resp := gh.WorkflowRuns{
			TotalCount: gh.Ptr(1),
			WorkflowRuns: []*gh.WorkflowRun{
				{ID: gh.Ptr(int64(100)), Status: gh.Ptr("in_progress")},
			},
		}
		json.NewEncoder(w).Encode(resp)
	})

	client, _ := newTestClient(t, mux)
	runs, err := client.Workflows().ListRuns(context.Background(), platform.RunFilters{Status: "in_progress"})

	require.NoError(t, err)
	assert.Len(t, runs, 1)
	assert.Equal(t, int64(100), runs[0].ID)
}

func TestWorkflowServiceCancelRun(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/test-org/test-repo/actions/runs/99/cancel", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})

	client, _ := newTestClient(t, mux)
	err := client.Workflows().CancelRun(context.Background(), 99)

	require.NoError(t, err)
}
