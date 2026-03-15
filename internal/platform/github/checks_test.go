package github

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	gh "github.com/google/go-github/v68/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetCombinedStatus_CommitStatusSuccess(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/commits/main/status", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(gh.CombinedStatus{State: gh.Ptr("success")})
	})
	mux.HandleFunc("GET /repos/test-org/test-repo/commits/main/check-runs", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(gh.ListCheckRunsResults{Total: gh.Ptr(0)})
	})

	client, _ := newTestClient(t, mux)
	status, err := client.Checks().GetCombinedStatus(context.Background(), "main")
	require.NoError(t, err)
	assert.Equal(t, "success", status)
}

func TestGetCombinedStatus_CheckRunFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/commits/main/status", func(w http.ResponseWriter, _ *http.Request) {
		// No commit statuses — empty/success
		json.NewEncoder(w).Encode(gh.CombinedStatus{State: gh.Ptr("success")})
	})
	mux.HandleFunc("GET /repos/test-org/test-repo/commits/main/check-runs", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(gh.ListCheckRunsResults{
			Total: gh.Ptr(1),
			CheckRuns: []*gh.CheckRun{
				{
					Name:       gh.Ptr("Cloudflare Pages"),
					Status:     gh.Ptr("completed"),
					Conclusion: gh.Ptr("failure"),
				},
			},
		})
	})

	client, _ := newTestClient(t, mux)
	status, err := client.Checks().GetCombinedStatus(context.Background(), "main")
	require.NoError(t, err)
	assert.Equal(t, "failure", status)
}

func TestGetCombinedStatus_CheckRunPending(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/commits/main/status", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(gh.CombinedStatus{State: gh.Ptr("success")})
	})
	mux.HandleFunc("GET /repos/test-org/test-repo/commits/main/check-runs", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(gh.ListCheckRunsResults{
			Total: gh.Ptr(1),
			CheckRuns: []*gh.CheckRun{
				{
					Name:   gh.Ptr("Cloudflare Pages"),
					Status: gh.Ptr("in_progress"),
				},
			},
		})
	})

	client, _ := newTestClient(t, mux)
	status, err := client.Checks().GetCombinedStatus(context.Background(), "main")
	require.NoError(t, err)
	assert.Equal(t, "pending", status)
}

func TestGetCombinedStatus_BothSucceed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/commits/main/status", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(gh.CombinedStatus{State: gh.Ptr("success")})
	})
	mux.HandleFunc("GET /repos/test-org/test-repo/commits/main/check-runs", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(gh.ListCheckRunsResults{
			Total: gh.Ptr(1),
			CheckRuns: []*gh.CheckRun{
				{
					Name:       gh.Ptr("CI"),
					Status:     gh.Ptr("completed"),
					Conclusion: gh.Ptr("success"),
				},
			},
		})
	})

	client, _ := newTestClient(t, mux)
	status, err := client.Checks().GetCombinedStatus(context.Background(), "main")
	require.NoError(t, err)
	assert.Equal(t, "success", status)
}

func TestGetCombinedStatus_CommitStatusFailureOverridesCheckRunSuccess(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/commits/main/status", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(gh.CombinedStatus{State: gh.Ptr("failure")})
	})
	mux.HandleFunc("GET /repos/test-org/test-repo/commits/main/check-runs", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(gh.ListCheckRunsResults{
			Total: gh.Ptr(1),
			CheckRuns: []*gh.CheckRun{
				{
					Name:       gh.Ptr("CI"),
					Status:     gh.Ptr("completed"),
					Conclusion: gh.Ptr("success"),
				},
			},
		})
	})

	client, _ := newTestClient(t, mux)
	status, err := client.Checks().GetCombinedStatus(context.Background(), "main")
	require.NoError(t, err)
	assert.Equal(t, "failure", status)
}

func TestGetCombinedStatus_NoStatusesOrChecks(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/commits/main/status", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(gh.CombinedStatus{})
	})
	mux.HandleFunc("GET /repos/test-org/test-repo/commits/main/check-runs", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(gh.ListCheckRunsResults{Total: gh.Ptr(0)})
	})

	client, _ := newTestClient(t, mux)
	status, err := client.Checks().GetCombinedStatus(context.Background(), "main")
	require.NoError(t, err)
	assert.Equal(t, "success", status)
}

func TestGetCombinedStatus_CheckRunCancelled(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/commits/main/status", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(gh.CombinedStatus{State: gh.Ptr("success")})
	})
	mux.HandleFunc("GET /repos/test-org/test-repo/commits/main/check-runs", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(gh.ListCheckRunsResults{
			Total: gh.Ptr(1),
			CheckRuns: []*gh.CheckRun{
				{
					Name:       gh.Ptr("CI"),
					Status:     gh.Ptr("completed"),
					Conclusion: gh.Ptr("cancelled"),
				},
			},
		})
	})

	client, _ := newTestClient(t, mux)
	status, err := client.Checks().GetCombinedStatus(context.Background(), "main")
	require.NoError(t, err)
	assert.Equal(t, "failure", status)
}
