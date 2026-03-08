package github

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIssueServiceCreate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/test-org/test-repo/issues", func(w http.ResponseWriter, r *http.Request) {
		var req gh.IssueRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "Test issue", *req.Title)
		assert.Equal(t, "Body text", *req.Body)
		assert.Equal(t, []string{"herd/type:feature"}, *req.Labels)
		assert.Equal(t, 5, *req.Milestone)

		resp := gh.Issue{
			Number:  gh.Ptr(42),
			Title:   gh.Ptr("Test issue"),
			Body:    gh.Ptr("Body text"),
			State:   gh.Ptr("open"),
			HTMLURL: gh.Ptr("https://github.com/test-org/test-repo/issues/42"),
		}
		json.NewEncoder(w).Encode(resp)
	})

	client, _ := newTestClient(t, mux)
	svc := client.Issues()
	milestone := 5
	issue, err := svc.Create(context.Background(), "Test issue", "Body text", []string{"herd/type:feature"}, &milestone)

	require.NoError(t, err)
	assert.Equal(t, 42, issue.Number)
	assert.Equal(t, "Test issue", issue.Title)
	assert.Equal(t, "open", issue.State)
}

func TestIssueServiceGet(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/issues/42", func(w http.ResponseWriter, r *http.Request) {
		resp := gh.Issue{
			Number: gh.Ptr(42),
			Title:  gh.Ptr("Found issue"),
			State:  gh.Ptr("open"),
		}
		json.NewEncoder(w).Encode(resp)
	})

	client, _ := newTestClient(t, mux)
	issue, err := client.Issues().Get(context.Background(), 42)

	require.NoError(t, err)
	assert.Equal(t, 42, issue.Number)
	assert.Equal(t, "Found issue", issue.Title)
}

func TestIssueServiceListFiltersPRs(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/issues", func(w http.ResponseWriter, r *http.Request) {
		resp := []*gh.Issue{
			{Number: gh.Ptr(1), Title: gh.Ptr("Real issue"), State: gh.Ptr("open")},
			{Number: gh.Ptr(2), Title: gh.Ptr("A PR"), State: gh.Ptr("open"),
				PullRequestLinks: &gh.PullRequestLinks{URL: gh.Ptr("https://api.github.com/pulls/2")}},
			{Number: gh.Ptr(3), Title: gh.Ptr("Another issue"), State: gh.Ptr("open")},
		}
		json.NewEncoder(w).Encode(resp)
	})

	client, _ := newTestClient(t, mux)
	issues, err := client.Issues().List(context.Background(), platform.IssueFilters{})

	require.NoError(t, err)
	assert.Len(t, issues, 2)
	assert.Equal(t, 1, issues[0].Number)
	assert.Equal(t, 3, issues[1].Number)
}

func TestIssueServiceListByMilestone(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/issues", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "5", r.URL.Query().Get("milestone"))
		resp := []*gh.Issue{
			{Number: gh.Ptr(10), Title: gh.Ptr("In milestone"), State: gh.Ptr("open")},
		}
		json.NewEncoder(w).Encode(resp)
	})

	client, _ := newTestClient(t, mux)
	milestone := 5
	issues, err := client.Issues().List(context.Background(), platform.IssueFilters{Milestone: &milestone})

	require.NoError(t, err)
	assert.Len(t, issues, 1)
}

func TestIssueServiceUpdate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /repos/test-org/test-repo/issues/42", func(w http.ResponseWriter, r *http.Request) {
		var req gh.IssueRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "closed", *req.State)

		resp := gh.Issue{
			Number: gh.Ptr(42),
			Title:  gh.Ptr("Updated"),
			State:  gh.Ptr("closed"),
		}
		json.NewEncoder(w).Encode(resp)
	})

	client, _ := newTestClient(t, mux)
	state := "closed"
	issue, err := client.Issues().Update(context.Background(), 42, platform.IssueUpdate{State: &state})

	require.NoError(t, err)
	assert.Equal(t, "closed", issue.State)
}

func TestIssueServiceAddLabels(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/test-org/test-repo/issues/42/labels", func(w http.ResponseWriter, r *http.Request) {
		var labels []string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&labels))
		assert.Equal(t, []string{"herd/status:ready"}, labels)

		resp := []*gh.Label{{Name: gh.Ptr("herd/status:ready")}}
		json.NewEncoder(w).Encode(resp)
	})

	client, _ := newTestClient(t, mux)
	err := client.Issues().AddLabels(context.Background(), 42, []string{"herd/status:ready"})

	require.NoError(t, err)
}

func TestIssueServiceRemoveLabels(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /repos/test-org/test-repo/issues/42/labels/herd/status:ready", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	client, _ := newTestClient(t, mux)
	err := client.Issues().RemoveLabels(context.Background(), 42, []string{"herd/status:ready"})

	require.NoError(t, err)
}

func TestIssueServiceRemoveLabelsIgnores404(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /repos/test-org/test-repo/issues/42/labels/missing", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"message": "Label does not exist"})
	})

	client, _ := newTestClient(t, mux)
	err := client.Issues().RemoveLabels(context.Background(), 42, []string{"missing"})

	// Should not return error for 404
	require.NoError(t, err)
}

func TestIssueServiceAddComment(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/test-org/test-repo/issues/42/comments", func(w http.ResponseWriter, r *http.Request) {
		var comment gh.IssueComment
		require.NoError(t, json.NewDecoder(r.Body).Decode(&comment))
		assert.Equal(t, "Worker failed", *comment.Body)

		resp := gh.IssueComment{ID: gh.Ptr(int64(1)), Body: gh.Ptr("Worker failed")}
		json.NewEncoder(w).Encode(resp)
	})

	client, _ := newTestClient(t, mux)
	err := client.Issues().AddComment(context.Background(), 42, "Worker failed")

	require.NoError(t, err)
}
