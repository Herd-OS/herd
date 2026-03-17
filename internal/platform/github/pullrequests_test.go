package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPullRequestCreate(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/test-org/test-repo/pulls", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var req gh.NewPullRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "Test PR", req.GetTitle())
		assert.Equal(t, "feature-branch", req.GetHead())
		assert.Equal(t, "main", req.GetBase())

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(gh.PullRequest{
			Number:  gh.Ptr(1),
			Title:   gh.Ptr("Test PR"),
			Body:    gh.Ptr("body"),
			State:   gh.Ptr("open"),
			Head:    &gh.PullRequestBranch{Ref: gh.Ptr("feature-branch")},
			Base:    &gh.PullRequestBranch{Ref: gh.Ptr("main")},
			HTMLURL: gh.Ptr("https://github.com/test-org/test-repo/pull/1"),
		})
	})

	client, _ := newTestClient(t, mux)
	pr, err := client.PullRequests().Create(context.Background(), "Test PR", "body", "feature-branch", "main")
	require.NoError(t, err)
	assert.Equal(t, 1, pr.Number)
	assert.Equal(t, "Test PR", pr.Title)
	assert.Equal(t, "feature-branch", pr.Head)
	assert.Equal(t, "main", pr.Base)
}

func TestPullRequestGet(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/test-org/test-repo/pulls/42", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(gh.PullRequest{
			Number:    gh.Ptr(42),
			Title:     gh.Ptr("Feature"),
			State:     gh.Ptr("open"),
			Mergeable: gh.Ptr(true),
			Head:      &gh.PullRequestBranch{Ref: gh.Ptr("feat")},
			Base:      &gh.PullRequestBranch{Ref: gh.Ptr("main")},
		})
	})

	client, _ := newTestClient(t, mux)
	pr, err := client.PullRequests().Get(context.Background(), 42)
	require.NoError(t, err)
	assert.Equal(t, 42, pr.Number)
	assert.True(t, pr.Mergeable)
}

func TestPullRequestList(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/test-org/test-repo/pulls", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "open", r.URL.Query().Get("state"))
		json.NewEncoder(w).Encode([]gh.PullRequest{
			{Number: gh.Ptr(1), Title: gh.Ptr("PR 1"), Head: &gh.PullRequestBranch{Ref: gh.Ptr("a")}, Base: &gh.PullRequestBranch{Ref: gh.Ptr("main")}},
			{Number: gh.Ptr(2), Title: gh.Ptr("PR 2"), Head: &gh.PullRequestBranch{Ref: gh.Ptr("b")}, Base: &gh.PullRequestBranch{Ref: gh.Ptr("main")}},
		})
	})

	client, _ := newTestClient(t, mux)
	prs, err := client.PullRequests().List(context.Background(), platform.PRFilters{State: "open"})
	require.NoError(t, err)
	assert.Len(t, prs, 2)
}

func TestPullRequestList_HeadFilterIncludesOwner(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/test-org/test-repo/pulls", func(w http.ResponseWriter, r *http.Request) {
		// GitHub API requires "owner:branch" format for head filter
		assert.Equal(t, "test-org:herd/batch/1-test", r.URL.Query().Get("head"))
		json.NewEncoder(w).Encode([]gh.PullRequest{})
	})

	client, _ := newTestClient(t, mux)
	_, err := client.PullRequests().List(context.Background(), platform.PRFilters{Head: "herd/batch/1-test"})
	require.NoError(t, err)
}

func TestPullRequestListPaginated(t *testing.T) {
	callCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/test-org/test-repo/pulls", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/test-org/test-repo/pulls?page=2>; rel="next"`, ""))
			json.NewEncoder(w).Encode([]gh.PullRequest{
				{Number: gh.Ptr(1), Title: gh.Ptr("PR 1"), Head: &gh.PullRequestBranch{Ref: gh.Ptr("a")}, Base: &gh.PullRequestBranch{Ref: gh.Ptr("main")}},
			})
		} else {
			json.NewEncoder(w).Encode([]gh.PullRequest{
				{Number: gh.Ptr(2), Title: gh.Ptr("PR 2"), Head: &gh.PullRequestBranch{Ref: gh.Ptr("b")}, Base: &gh.PullRequestBranch{Ref: gh.Ptr("main")}},
			})
		}
	})

	client, _ := newTestClient(t, mux)
	prs, err := client.PullRequests().List(context.Background(), platform.PRFilters{})
	require.NoError(t, err)
	assert.Len(t, prs, 2)
	assert.Equal(t, 2, callCount)
}

func TestPullRequestUpdate(t *testing.T) {
	tests := []struct {
		name  string
		title *string
		body  *string
	}{
		{name: "both", title: gh.Ptr("New Title"), body: gh.Ptr("New Body")},
		{name: "title only", title: gh.Ptr("New Title"), body: nil},
		{name: "body only", title: nil, body: gh.Ptr("New Body")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/repos/test-org/test-repo/pulls/1", func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPatch, r.Method)
				json.NewEncoder(w).Encode(gh.PullRequest{
					Number: gh.Ptr(1),
					Title:  gh.Ptr("Updated"),
					Head:   &gh.PullRequestBranch{Ref: gh.Ptr("a")},
					Base:   &gh.PullRequestBranch{Ref: gh.Ptr("main")},
				})
			})

			client, _ := newTestClient(t, mux)
			pr, err := client.PullRequests().Update(context.Background(), 1, tt.title, tt.body)
			require.NoError(t, err)
			assert.Equal(t, 1, pr.Number)
		})
	}
}

func TestPullRequestMerge(t *testing.T) {
	tests := []struct {
		name   string
		method platform.MergeMethod
	}{
		{"squash", platform.MergeMethodSquash},
		{"merge", platform.MergeMethodMerge},
		{"rebase", platform.MergeMethodRebase},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/repos/test-org/test-repo/pulls/1/merge", func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPut, r.Method)
				var req struct {
					MergeMethod string `json:"merge_method"`
				}
				json.NewDecoder(r.Body).Decode(&req)
				assert.Equal(t, string(tt.method), req.MergeMethod)

				json.NewEncoder(w).Encode(gh.PullRequestMergeResult{
					SHA:     gh.Ptr("abc123"),
					Merged:  gh.Ptr(true),
					Message: gh.Ptr("Merged"),
				})
			})

			client, _ := newTestClient(t, mux)
			result, err := client.PullRequests().Merge(context.Background(), 1, tt.method)
			require.NoError(t, err)
			assert.True(t, result.Merged)
			assert.Equal(t, "abc123", result.SHA)
		})
	}
}

func TestPullRequestCreateReview(t *testing.T) {
	tests := []struct {
		name  string
		event platform.ReviewEvent
	}{
		{"approve", platform.ReviewApprove},
		{"request changes", platform.ReviewRequestChanges},
		{"comment", platform.ReviewComment},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/repos/test-org/test-repo/pulls/1/reviews", func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				var req gh.PullRequestReviewRequest
				json.NewDecoder(r.Body).Decode(&req)
				assert.Equal(t, string(tt.event), req.GetEvent())

				json.NewEncoder(w).Encode(gh.PullRequestReview{
					ID: gh.Ptr(int64(1)),
				})
			})

			client, _ := newTestClient(t, mux)
			err := client.PullRequests().CreateReview(context.Background(), 1, "Review body", tt.event)
			require.NoError(t, err)
		})
	}
}

func TestPullRequestUpdateBranch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/test-org/test-repo/pulls/1/update-branch", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"message": "Updating"})
	})

	client, _ := newTestClient(t, mux)
	err := client.PullRequests().UpdateBranch(context.Background(), 1)
	require.NoError(t, err)
}

func TestPullRequestAddComment(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/test-org/test-repo/issues/42/comments", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		var req gh.IssueComment
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "Test comment", req.GetBody())

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(gh.IssueComment{ID: gh.Ptr(int64(1)), Body: gh.Ptr("Test comment")})
	})

	client, _ := newTestClient(t, mux)
	err := client.PullRequests().AddComment(context.Background(), 42, "Test comment")
	require.NoError(t, err)
}

func TestPullRequestGetDiff(t *testing.T) {
	mux := http.NewServeMux()
	// go-github's GetRaw sends Accept header for diff format
	mux.HandleFunc("GET /repos/test-org/test-repo/pulls/42", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("diff --git a/file.go b/file.go\n+added line\n"))
	})

	client, _ := newTestClient(t, mux)
	diff, err := client.PullRequests().GetDiff(context.Background(), 42)
	require.NoError(t, err)
	assert.Contains(t, diff, "diff --git")
	assert.Contains(t, diff, "+added line")
}

func TestMapPullRequest(t *testing.T) {
	ts := gh.Timestamp{Time: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)}
	pr := mapPullRequest(&gh.PullRequest{
		Number:    gh.Ptr(42),
		Title:     gh.Ptr("Test"),
		Body:      gh.Ptr("Body"),
		State:     gh.Ptr("open"),
		Head:      &gh.PullRequestBranch{Ref: gh.Ptr("feature")},
		Base:      &gh.PullRequestBranch{Ref: gh.Ptr("main")},
		Mergeable: gh.Ptr(true),
		HTMLURL:   gh.Ptr("https://github.com/org/repo/pull/42"),
		CreatedAt: &ts,
	})

	assert.Equal(t, 42, pr.Number)
	assert.Equal(t, "Test", pr.Title)
	assert.Equal(t, "Body", pr.Body)
	assert.Equal(t, "open", pr.State)
	assert.Equal(t, "feature", pr.Head)
	assert.Equal(t, "main", pr.Base)
	assert.True(t, pr.Mergeable)
	assert.Equal(t, "https://github.com/org/repo/pull/42", pr.URL)
	assert.Equal(t, time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC), pr.CreatedAt)
}
