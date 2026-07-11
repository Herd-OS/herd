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

func TestRegisterRepositoryPostsSetupTokenOnlyToRegisterEndpoint(t *testing.T) {
	var gotPath string
	var got RegisterRepositoryRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_ = json.NewEncoder(w).Encode(RegisterRepositoryResponse{
			RepositoryID:         12,
			InstallationID:       34,
			RunnerBootstrapToken: "bootstrap",
		})
	}))
	defer server.Close()

	c, err := New(server.URL, server.Client())
	require.NoError(t, err)

	resp, err := c.RegisterRepository(context.Background(), RegisterRepositoryRequest{
		Repository: "octo/repo",
		Owner:      "octo",
		Name:       "repo",
		SetupToken: "setup-token",
		AppLogin:   "herd-os",
	})

	require.NoError(t, err)
	assert.Equal(t, "/api/v1/github/repositories/register", gotPath)
	assert.Equal(t, "setup-token", got.SetupToken)
	assert.Equal(t, int64(12), resp.RepositoryID)
	assert.Equal(t, "bootstrap", resp.RunnerBootstrapToken)
}

func TestRegisterRepositoryReturnsActionableServiceError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "install @herd-os on octo/repo"})
	}))
	defer server.Close()

	c, err := New(server.URL, server.Client())
	require.NoError(t, err)

	_, err = c.RegisterRepository(context.Background(), RegisterRepositoryRequest{SetupToken: "token"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "install @herd-os on octo/repo")
}

func TestRegisterRepositoryRejectsMissingBootstrapToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(RegisterRepositoryResponse{RepositoryID: 1})
	}))
	defer server.Close()

	c, err := New(server.URL, server.Client())
	require.NoError(t, err)

	_, err = c.RegisterRepository(context.Background(), RegisterRepositoryRequest{SetupToken: "token"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing runner bootstrap token")
}

func TestNewValidatesBaseURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{name: "empty", url: ""},
		{name: "relative", url: "/api"},
		{name: "unsupported scheme", url: "ftp://example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.url, nil)
			require.Error(t, err)
		})
	}
}
