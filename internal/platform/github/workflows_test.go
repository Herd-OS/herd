package github

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
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
	workflowMetadataCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/actions/runs/99", func(w http.ResponseWriter, r *http.Request) {
		resp := gh.WorkflowRun{
			ID:         gh.Ptr(int64(99)),
			Name:       gh.Ptr("CI run for abc123"),
			WorkflowID: gh.Ptr(int64(321)),
			Path:       gh.Ptr(".github/workflows/old-ci.yml"),
			HeadBranch: gh.Ptr("herd/worker/99"),
			HeadSHA:    gh.Ptr("abc123"),
			Status:     gh.Ptr("completed"),
			Conclusion: gh.Ptr("success"),
			HTMLURL:    gh.Ptr("https://github.com/test-org/test-repo/actions/runs/99"),
			CreatedAt:  &ts,
		}
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("GET /repos/test-org/test-repo/actions/workflows/321", func(w http.ResponseWriter, r *http.Request) {
		workflowMetadataCalls++
		resp := gh.Workflow{
			ID:   gh.Ptr(int64(321)),
			Name: gh.Ptr("CI"),
			Path: gh.Ptr(".github/workflows/ci.yml"),
		}
		json.NewEncoder(w).Encode(resp)
	})

	client, _ := newTestClient(t, mux)
	run, err := client.Workflows().GetRun(context.Background(), 99)

	require.NoError(t, err)
	assert.Equal(t, int64(99), run.ID)
	assert.Equal(t, int64(321), run.WorkflowID)
	assert.Equal(t, "CI", run.WorkflowName)
	assert.Equal(t, ".github/workflows/ci.yml", run.WorkflowPath)
	assert.Equal(t, "herd/worker/99", run.HeadBranch)
	assert.Equal(t, "abc123", run.HeadSHA)
	assert.Equal(t, "completed", run.Status)
	assert.Equal(t, "success", run.Conclusion)
	assert.Equal(t, time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC), run.CreatedAt)
	assert.Equal(t, 1, workflowMetadataCalls)
}

func TestWorkflowServiceGetRunDiagnostics(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/actions/runs/99", func(w http.ResponseWriter, r *http.Request) {
		resp := gh.WorkflowRun{
			ID:         gh.Ptr(int64(99)),
			Name:       gh.Ptr("CI"),
			HeadBranch: gh.Ptr("herd/worker/99"),
			HeadSHA:    gh.Ptr("abc123"),
			Conclusion: gh.Ptr("failure"),
			HTMLURL:    gh.Ptr("https://github.com/test-org/test-repo/actions/runs/99"),
		}
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	})
	mux.HandleFunc("GET /repos/test-org/test-repo/actions/runs/99/jobs", func(w http.ResponseWriter, r *http.Request) {
		resp := gh.Jobs{
			TotalCount: gh.Ptr(2),
			Jobs: []*gh.WorkflowJob{
				{
					ID:          gh.Ptr(int64(501)),
					Name:        gh.Ptr("test"),
					Status:      gh.Ptr("completed"),
					Conclusion:  gh.Ptr("failure"),
					HTMLURL:     gh.Ptr("https://github.com/test-org/test-repo/actions/runs/99/job/501"),
					CheckRunURL: gh.Ptr("https://api.github.com/repos/test-org/test-repo/check-runs/9001"),
				},
				{
					ID:         gh.Ptr(int64(502)),
					Name:       gh.Ptr("lint"),
					Status:     gh.Ptr("completed"),
					Conclusion: gh.Ptr("success"),
					HTMLURL:    gh.Ptr("https://github.com/test-org/test-repo/actions/runs/99/job/502"),
				},
			},
		}
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	})
	mux.HandleFunc("GET /repos/test-org/test-repo/check-runs/9001/annotations", func(w http.ResponseWriter, r *http.Request) {
		resp := []*gh.CheckRunAnnotation{{Message: gh.Ptr("expected 2, got 1")}}
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	})
	mux.HandleFunc("GET /repos/test-org/test-repo/actions/jobs/501/logs", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/logs/job-501", http.StatusFound)
	})
	mux.HandleFunc("GET /logs/job-501", func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write([]byte("line 1\nline 2\nfailure details"))
		require.NoError(t, err)
	})

	client, _ := newTestClient(t, mux)
	diagnostics, err := client.Workflows().GetRunDiagnostics(context.Background(), 99)

	require.NoError(t, err)
	require.NotNil(t, diagnostics)
	assert.Equal(t, int64(99), diagnostics.RunID)
	assert.Equal(t, "CI", diagnostics.Workflow)
	assert.Equal(t, "https://github.com/test-org/test-repo/actions/runs/99", diagnostics.URL)
	assert.Equal(t, "failure", diagnostics.Conclusion)
	assert.Equal(t, "herd/worker/99", diagnostics.HeadBranch)
	assert.Equal(t, "abc123", diagnostics.HeadSHA)
	assert.Equal(t, []platform.WorkflowJobDiagnostic{
		{
			ID:         501,
			Name:       "test",
			URL:        "https://github.com/test-org/test-repo/actions/runs/99/job/501",
			Conclusion: "failure",
			Status:     "completed",
		},
		{
			ID:         502,
			Name:       "lint",
			URL:        "https://github.com/test-org/test-repo/actions/runs/99/job/502",
			Conclusion: "success",
			Status:     "completed",
		},
	}, diagnostics.Jobs)
	assert.Equal(t, []string{"test: expected 2, got 1"}, diagnostics.Annotations)
	assert.Equal(t, "available", diagnostics.LogStatus)
	assert.Equal(t, "line 1\nline 2\nfailure details", diagnostics.LogExcerpt)
}

func TestWorkflowServiceGetRunDiagnostics_LogUnavailableReturnsDiagnostics(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/actions/runs/99", func(w http.ResponseWriter, r *http.Request) {
		resp := gh.WorkflowRun{
			ID:         gh.Ptr(int64(99)),
			Name:       gh.Ptr("CI"),
			Conclusion: gh.Ptr("failure"),
			HTMLURL:    gh.Ptr("https://github.com/test-org/test-repo/actions/runs/99"),
		}
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	})
	mux.HandleFunc("GET /repos/test-org/test-repo/actions/runs/99/jobs", func(w http.ResponseWriter, r *http.Request) {
		resp := gh.Jobs{
			TotalCount: gh.Ptr(1),
			Jobs: []*gh.WorkflowJob{
				{
					ID:         gh.Ptr(int64(501)),
					Name:       gh.Ptr("test"),
					Status:     gh.Ptr("completed"),
					Conclusion: gh.Ptr("failure"),
					HTMLURL:    gh.Ptr("https://github.com/test-org/test-repo/actions/runs/99/job/501"),
				},
			},
		}
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	})
	mux.HandleFunc("GET /repos/test-org/test-repo/actions/jobs/501/logs", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusGone)
	})

	client, _ := newTestClient(t, mux)
	diagnostics, err := client.Workflows().GetRunDiagnostics(context.Background(), 99)

	require.NoError(t, err)
	require.NotNil(t, diagnostics)
	assert.Equal(t, "CI", diagnostics.Workflow)
	assert.Equal(t, []platform.WorkflowJobDiagnostic{
		{
			ID:         501,
			Name:       "test",
			URL:        "https://github.com/test-org/test-repo/actions/runs/99/job/501",
			Conclusion: "failure",
			Status:     "completed",
		},
	}, diagnostics.Jobs)
	assert.Equal(t, "unavailable", diagnostics.LogStatus)
	assert.True(t, strings.Contains(diagnostics.LogExcerpt, "workflow logs unavailable:"))
}

func TestWorkflowServiceGetRunDiagnostics_MissingRun(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/actions/runs/404", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	client, _ := newTestClient(t, mux)
	diagnostics, err := client.Workflows().GetRunDiagnostics(context.Background(), 404)

	require.Error(t, err)
	assert.Nil(t, diagnostics)
	assert.Contains(t, err.Error(), "getting run diagnostics for run 404")
}

func TestWorkflowServiceListRuns(t *testing.T) {
	workflowMetadataCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "in_progress", r.URL.Query().Get("status"))
		resp := gh.WorkflowRuns{
			TotalCount: gh.Ptr(2),
			WorkflowRuns: []*gh.WorkflowRun{
				{
					ID:         gh.Ptr(int64(100)),
					WorkflowID: gh.Ptr(int64(321)),
					Name:       gh.Ptr("Display title from run-name"),
					Path:       gh.Ptr(".github/workflows/old-ci.yml"),
					Status:     gh.Ptr("in_progress"),
				},
				{
					ID:         gh.Ptr(int64(101)),
					WorkflowID: gh.Ptr(int64(322)),
					Name:       gh.Ptr("Another display title"),
					Path:       gh.Ptr(".github/workflows/another.yml"),
					Status:     gh.Ptr("in_progress"),
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("GET /repos/test-org/test-repo/actions/workflows/321", func(w http.ResponseWriter, r *http.Request) {
		workflowMetadataCalls++
		json.NewEncoder(w).Encode(gh.Workflow{
			ID:   gh.Ptr(int64(321)),
			Name: gh.Ptr("CI"),
			Path: gh.Ptr(".github/workflows/ci.yml"),
		})
	})
	mux.HandleFunc("GET /repos/test-org/test-repo/actions/workflows/322", func(w http.ResponseWriter, r *http.Request) {
		workflowMetadataCalls++
		json.NewEncoder(w).Encode(gh.Workflow{
			ID:   gh.Ptr(int64(322)),
			Name: gh.Ptr("Lint"),
			Path: gh.Ptr(".github/workflows/lint.yml"),
		})
	})

	client, _ := newTestClient(t, mux)
	runs, err := client.Workflows().ListRuns(context.Background(), platform.RunFilters{Status: "in_progress"})

	require.NoError(t, err)
	require.Len(t, runs, 2)
	assert.Equal(t, int64(100), runs[0].ID)
	assert.Equal(t, "Display title from run-name", runs[0].WorkflowName)
	assert.Equal(t, ".github/workflows/old-ci.yml", runs[0].WorkflowPath)
	assert.Equal(t, 0, workflowMetadataCalls)
}

func TestWorkflowServiceListRuns_ResolveWorkflowIdentity(t *testing.T) {
	workflowMetadataCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		resp := gh.WorkflowRuns{
			TotalCount: gh.Ptr(1),
			WorkflowRuns: []*gh.WorkflowRun{
				{
					ID:         gh.Ptr(int64(100)),
					WorkflowID: gh.Ptr(int64(321)),
					Name:       gh.Ptr("Display title from run-name"),
					Path:       gh.Ptr(".github/workflows/old-ci.yml"),
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("GET /repos/test-org/test-repo/actions/workflows/321", func(w http.ResponseWriter, r *http.Request) {
		workflowMetadataCalls++
		resp := gh.Workflow{
			ID:   gh.Ptr(int64(321)),
			Name: gh.Ptr("CI"),
			Path: gh.Ptr(".github/workflows/ci.yml"),
		}
		json.NewEncoder(w).Encode(resp)
	})

	client, _ := newTestClient(t, mux)
	runs, err := client.Workflows().ListRuns(context.Background(), platform.RunFilters{
		ResolveWorkflowIdentity: true,
	})

	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, "CI", runs[0].WorkflowName)
	assert.Equal(t, ".github/workflows/ci.yml", runs[0].WorkflowPath)
	assert.Equal(t, 1, workflowMetadataCalls)
}

func TestWorkflowServiceListRuns_ByWorkflowFileName(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/actions/workflows/herd-worker.yml/runs", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "in_progress", r.URL.Query().Get("status"))
		resp := gh.WorkflowRuns{
			TotalCount: gh.Ptr(1),
			WorkflowRuns: []*gh.WorkflowRun{
				{ID: gh.Ptr(int64(200)), Status: gh.Ptr("in_progress")},
			},
		}
		json.NewEncoder(w).Encode(resp)
	})

	client, _ := newTestClient(t, mux)
	runs, err := client.Workflows().ListRuns(context.Background(), platform.RunFilters{
		Status:           "in_progress",
		WorkflowFileName: "herd-worker.yml",
	})

	require.NoError(t, err)
	assert.Len(t, runs, 1)
	assert.Equal(t, int64(200), runs[0].ID)
}

func TestWorkflowServiceGetRun_ParsesRunName(t *testing.T) {
	ts := gh.Timestamp{Time: time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/actions/runs/99", func(w http.ResponseWriter, r *http.Request) {
		resp := gh.WorkflowRun{
			ID:           gh.Ptr(int64(99)),
			Name:         gh.Ptr("HerdOS Worker"),
			DisplayTitle: gh.Ptr("Herd Worker #42"),
			Status:       gh.Ptr("completed"),
			CreatedAt:    &ts,
		}
		json.NewEncoder(w).Encode(resp)
	})

	client, _ := newTestClient(t, mux)
	run, err := client.Workflows().GetRun(context.Background(), 99)

	require.NoError(t, err)
	assert.Equal(t, map[string]string{"issue_number": "42"}, run.Inputs)
}

func TestParseRunNameInputs(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  map[string]string
	}{
		{"valid", "Herd Worker #42", map[string]string{"issue_number": "42"}},
		{"large number", "Herd Worker #12345", map[string]string{"issue_number": "12345"}},
		{"empty", "", nil},
		{"wrong prefix", "worker #42", nil},
		{"no number", "Herd Worker #", nil},
		{"non-numeric", "Herd Worker #abc", nil},
		{"mixed", "Herd Worker #42abc", nil},
		{"just name", "HerdOS Worker", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRunNameInputs(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
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
