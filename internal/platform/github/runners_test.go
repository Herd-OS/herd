package github

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	gh "github.com/google/go-github/v68/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunnerServiceList(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/actions/runners", func(w http.ResponseWriter, r *http.Request) {
		resp := gh.Runners{
			TotalCount: 2,
			Runners: []*gh.Runner{
				{
					ID:     gh.Ptr(int64(1)),
					Name:   gh.Ptr("herd-worker-1"),
					Status: gh.Ptr("online"),
					Busy:   gh.Ptr(true),
					Labels: []*gh.RunnerLabels{{Name: gh.Ptr("herd-worker")}},
				},
				{
					ID:     gh.Ptr(int64(2)),
					Name:   gh.Ptr("herd-worker-2"),
					Status: gh.Ptr("offline"),
					Busy:   gh.Ptr(false),
					Labels: []*gh.RunnerLabels{{Name: gh.Ptr("herd-worker")}},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	})

	client, _ := newTestClient(t, mux)
	runners, err := client.Runners().List(context.Background())

	require.NoError(t, err)
	assert.Len(t, runners, 2)
	assert.Equal(t, "herd-worker-1", runners[0].Name)
	assert.True(t, runners[0].Busy)
	assert.Equal(t, "herd-worker-2", runners[1].Name)
	assert.False(t, runners[1].Busy)
}

func TestRunnerServiceGet(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/test-org/test-repo/actions/runners/1", func(w http.ResponseWriter, r *http.Request) {
		resp := gh.Runner{
			ID:     gh.Ptr(int64(1)),
			Name:   gh.Ptr("herd-worker-1"),
			Status: gh.Ptr("online"),
			Busy:   gh.Ptr(false),
			Labels: []*gh.RunnerLabels{{Name: gh.Ptr("self-hosted")}, {Name: gh.Ptr("herd-worker")}},
		}
		json.NewEncoder(w).Encode(resp)
	})

	client, _ := newTestClient(t, mux)
	runner, err := client.Runners().Get(context.Background(), 1)

	require.NoError(t, err)
	assert.Equal(t, int64(1), runner.ID)
	assert.Equal(t, "herd-worker-1", runner.Name)
	assert.Equal(t, []string{"self-hosted", "herd-worker"}, runner.Labels)
}

func TestCreateRunnerRegistrationToken(t *testing.T) {
	expiresAt := time.Date(2026, 7, 11, 13, 0, 0, 0, time.UTC)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/test-org/test-repo/actions/runners/registration-token", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		json.NewEncoder(w).Encode(map[string]string{
			"token":      "runner-registration-token",
			"expires_at": expiresAt.Format(time.RFC3339),
		})
	})
	client, _ := newTestClient(t, mux)

	token, err := CreateRunnerRegistrationToken(context.Background(), client.gh, "test-org", "test-repo")

	require.NoError(t, err)
	assert.Equal(t, "runner-registration-token", token.Token)
	assert.Equal(t, expiresAt, token.ExpiresAt)
}

func TestCreateRunnerRegistrationTokenFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/test-org/test-repo/actions/runners/registration-token", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusBadGateway)
	})
	client, _ := newTestClient(t, mux)

	token, err := CreateRunnerRegistrationToken(context.Background(), client.gh, "test-org", "test-repo")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating runner registration token")
	assert.Zero(t, token)
}
