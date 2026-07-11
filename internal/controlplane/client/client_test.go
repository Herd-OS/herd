package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
		})
	}
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
