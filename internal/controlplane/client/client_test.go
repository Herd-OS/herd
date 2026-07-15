package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisterRepository(t *testing.T) {
	var got RegisterRepositoryRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, registerRepositoryPath, r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_ = json.NewEncoder(w).Encode(RegisterRepositoryResponse{
			RepositoryID:         10,
			InstallationID:       20,
			RunnerBootstrapToken: "hrb_test",
		})
	}))
	defer srv.Close()

	c, err := New(srv.URL, srv.Client())
	require.NoError(t, err)
	resp, err := c.RegisterRepository(context.Background(), RegisterRepositoryRequest{
		Repository: "octo/herd",
		Owner:      "octo",
		Name:       "herd",
		SetupToken: "gho_setup",
		AppLogin:   "herd-os",
	})

	require.NoError(t, err)
	assert.Equal(t, int64(10), resp.RepositoryID)
	assert.Equal(t, "gho_setup", got.SetupToken)
	assert.Equal(t, "herd-os", got.AppLogin)
}

func TestSubmitJobResultWithRetryRetriesTransientFailures(t *testing.T) {
	attempts := 0
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		gotAuth = r.Header.Get("Authorization")
		assert.Equal(t, "/api/v1/jobs/job-1/results", r.URL.Path)
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("try later"))
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	c, err := New(srv.URL, srv.Client())
	require.NoError(t, err)
	var sleeps []time.Duration

	err = c.SubmitJobResultWithRetry(context.Background(), "job-1", []byte(`{"ok":true}`), "token", RetryOptions{
		MaxAttempts:    4,
		InitialBackoff: time.Second,
		MaxBackoff:     3 * time.Second,
		Sleep: func(_ context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			return nil
		},
	})

	require.NoError(t, err)
	assert.Equal(t, 3, attempts)
	assert.Equal(t, "Bearer token", gotAuth)
	assert.Equal(t, []time.Duration{time.Second, 2 * time.Second}, sleeps)
}

func TestSubmitJobResultWithRetryDoesNotRetryBadRequest(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte("stale head"))
	}))
	defer srv.Close()
	c, err := New(srv.URL, srv.Client())
	require.NoError(t, err)

	err = c.SubmitJobResultWithRetry(context.Background(), "job-1", []byte(`{"ok":true}`), "", RetryOptions{
		MaxAttempts:    3,
		InitialBackoff: time.Millisecond,
		Sleep: func(context.Context, time.Duration) error {
			require.Fail(t, "sleep should not be called for non-retryable status")
			return nil
		},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "stale head")
	assert.Equal(t, 1, attempts)
}

func TestBoundedExponentialBackoffCapsDelay(t *testing.T) {
	tests := []struct {
		name    string
		attempt int
		want    time.Duration
	}{
		{name: "first retry", attempt: 1, want: time.Second},
		{name: "doubles", attempt: 3, want: 4 * time.Second},
		{name: "caps", attempt: 6, want: 5 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, BoundedExponentialBackoff(tt.attempt, time.Second, 5*time.Second))
		})
	}
}

func TestRegisterRepositoryErrors(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantErrSub string
	}{
		{"json error body", http.StatusForbidden, `{"error":"admin required"}`, "admin required"},
		{"plain error body", http.StatusServiceUnavailable, `unavailable`, "unavailable"},
		{"missing response field", http.StatusOK, `{"repository_id":1,"installation_id":2}`, "missing repository_id"},
		{"malformed success json", http.StatusOK, `{`, "decode repository registration response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			c, err := New(srv.URL, srv.Client())
			require.NoError(t, err)
			_, err = c.RegisterRepository(context.Background(), RegisterRepositoryRequest{Owner: "o", Name: "r", SetupToken: "token"})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErrSub)
			if tt.status >= 300 {
				var statusErr StatusError
				require.ErrorAs(t, err, &statusErr)
				assert.Equal(t, tt.status, statusErr.StatusCode)
			}
		})
	}
}

func TestRegisterRepositoryErrorRedactsSetupToken(t *testing.T) {
	setupToken := "gho_setup_secret_1234567890"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"registration failed for setup_token=` + setupToken + ` and github_pat_extra_secret"}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, srv.Client())
	require.NoError(t, err)

	_, err = c.RegisterRepository(context.Background(), RegisterRepositoryRequest{Owner: "o", Name: "r", SetupToken: setupToken})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "registration failed")
	assert.NotContains(t, err.Error(), setupToken)
	assert.NotContains(t, err.Error(), "github_pat_extra_secret")
	assert.Contains(t, err.Error(), "[REDACTED]")
}

func TestNewRejectsInvalidURL(t *testing.T) {
	tests := []string{"", "://bad", "ftp://example.com"}
	for _, in := range tests {
		t.Run(in, func(t *testing.T) {
			_, err := New(in, nil)
			require.Error(t, err)
		})
	}
}
