package github

import (
	"net/http"
	"net/http/httptest"
	"testing"

	gh "github.com/google/go-github/v68/github"
)

// newTestClient creates a Client backed by a mock HTTP server.
// The caller provides a mux to register handlers for specific API endpoints.
// Returns the client and a cleanup function.
func newTestClient(t *testing.T, mux *http.ServeMux) (*Client, *httptest.Server) { //nolint:unparam // server returned for future use
	t.Helper()

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	ghClient := gh.NewClient(nil)
	ghClient.BaseURL, _ = ghClient.BaseURL.Parse(server.URL + "/")
	ghClient.UploadURL, _ = ghClient.UploadURL.Parse(server.URL + "/")

	return &Client{
		gh:    ghClient,
		owner: "test-org",
		repo:  "test-repo",
	}, server
}
