package review

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppGitHubClientCreateCommitStatus(t *testing.T) {
	client := newReviewTestGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/repos/octo/widgets/statuses/head", r.URL.Path)
		var req gh.RepoStatus
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "pending", req.GetState())
		assert.Equal(t, HerdReviewContext, req.GetContext())
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(gh.RepoStatus{})
	}))

	err := client.CreateCommitStatus(context.Background(), 99, "octo", "widgets", "head", platform.CommitStatus{State: "pending", Context: HerdReviewContext})

	require.NoError(t, err)
}

func TestAppGitHubClientFindCommitStatusPaginates(t *testing.T) {
	client := newReviewTestGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/repos/octo/widgets/commits/head/statuses", r.URL.Path)
		switch r.URL.Query().Get("page") {
		case "":
			w.Header().Set("Link", `<http://`+r.Host+`/repos/octo/widgets/commits/head/statuses?page=2>; rel="next"`)
			json.NewEncoder(w).Encode([]gh.RepoStatus{{
				Context:     gh.Ptr(HerdReviewContext),
				State:       gh.Ptr("pending"),
				Description: gh.Ptr("different"),
				TargetURL:   gh.Ptr("https://example.test/run"),
			}})
		case "2":
			json.NewEncoder(w).Encode([]gh.RepoStatus{{
				Context:     gh.Ptr(HerdReviewContext),
				State:       gh.Ptr("success"),
				Description: gh.Ptr("done"),
				TargetURL:   gh.Ptr("https://example.test/run"),
			}})
		default:
			assert.Failf(t, "unexpected page", "page=%q", r.URL.Query().Get("page"))
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	found, err := client.FindCommitStatus(context.Background(), 99, "octo", "widgets", "head", platform.CommitStatus{
		State:       "success",
		Context:     HerdReviewContext,
		Description: "done",
		TargetURL:   "https://example.test/run",
	})

	require.NoError(t, err)
	assert.True(t, found)
}

func TestAppGitHubClientGetPullRequest(t *testing.T) {
	client := newReviewTestGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/octo/widgets/pulls/42", r.URL.Path)
		json.NewEncoder(w).Encode(gh.PullRequest{
			Number:  gh.Ptr(42),
			Title:   gh.Ptr("Batch"),
			State:   gh.Ptr("open"),
			Head:    &gh.PullRequestBranch{Ref: gh.Ptr("branch"), SHA: gh.Ptr("head")},
			Base:    &gh.PullRequestBranch{Ref: gh.Ptr("main")},
			HTMLURL: gh.Ptr("https://github.test/octo/widgets/pull/42"),
		})
	}))

	pr, err := client.GetPullRequest(context.Background(), 99, "octo", "widgets", 42)

	require.NoError(t, err)
	assert.Equal(t, 42, pr.Number)
	assert.Equal(t, "head", pr.HeadSHA)
	assert.Equal(t, "https://github.test/octo/widgets/pull/42", pr.URL)
}

func TestAppGitHubClientCreateReviewForCommit(t *testing.T) {
	client := newReviewTestGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/octo/widgets/pulls/42/reviews", r.URL.Path)
		var req gh.PullRequestReviewRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "REQUEST_CHANGES", req.GetEvent())
		assert.Equal(t, "head", req.GetCommitID())
		json.NewEncoder(w).Encode(gh.PullRequestReview{ID: gh.Ptr(int64(1))})
	}))

	err := client.CreateReviewForCommit(context.Background(), 99, "octo", "widgets", 42, "body", platform.ReviewRequestChanges, "head")

	require.NoError(t, err)
}

func TestAppGitHubClientFindReviewForCommitPaginates(t *testing.T) {
	client := newReviewTestGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/repos/octo/widgets/pulls/42/reviews", r.URL.Path)
		switch r.URL.Query().Get("page") {
		case "":
			w.Header().Set("Link", `<http://`+r.Host+`/repos/octo/widgets/pulls/42/reviews?page=2>; rel="next"`)
			json.NewEncoder(w).Encode([]gh.PullRequestReview{{
				CommitID: gh.Ptr("head"),
				Body:     gh.Ptr("different"),
				State:    gh.Ptr("COMMENTED"),
			}})
		case "2":
			json.NewEncoder(w).Encode([]gh.PullRequestReview{{
				CommitID: gh.Ptr("head"),
				Body:     gh.Ptr("body"),
				State:    gh.Ptr("COMMENTED"),
			}})
		default:
			assert.Failf(t, "unexpected page", "page=%q", r.URL.Query().Get("page"))
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	found, err := client.FindReviewForCommit(context.Background(), 99, "octo", "widgets", 42, " body\n", platform.ReviewCommentEvent, "head")

	require.NoError(t, err)
	assert.True(t, found)
}

func TestAppGitHubClientAddPullRequestComment(t *testing.T) {
	client := newReviewTestGitHub(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/octo/widgets/issues/42/comments", r.URL.Path)
		var req gh.IssueComment
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "body", req.GetBody())
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(gh.IssueComment{ID: gh.Ptr(int64(1))})
	}))

	err := client.AddPullRequestComment(context.Background(), 99, "octo", "widgets", 42, "body")

	require.NoError(t, err)
}

func newReviewTestGitHub(t *testing.T, handler http.Handler) AppGitHubClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return AppGitHubClient{NewClient: func(context.Context, int64) (*gh.Client, error) {
		client := gh.NewClient(nil)
		client.BaseURL, _ = client.BaseURL.Parse(server.URL + "/")
		client.UploadURL, _ = client.UploadURL.Parse(server.URL + "/")
		return client, nil
	}}
}
