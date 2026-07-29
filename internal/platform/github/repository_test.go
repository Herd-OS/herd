package github

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepositoryGuardedBranchMutationsUseConditionalRequest(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(context.Context, platform.RepositoryService) error
	}{
		{
			name: "update conflict after ref changes",
			mutate: func(ctx context.Context, repo platform.RepositoryService) error {
				return repo.(*repositoryService).UpdateBranchToCommitIfHead(ctx, "feature", "new", "old", true)
			},
		},
		{
			name: "delete conflict after ref changes",
			mutate: func(ctx context.Context, repo platform.RepositoryService) error {
				return repo.(*repositoryService).DeleteBranchIfHead(ctx, "feature", "old")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			liveSHA := "old"
			liveETag := `"old-etag"`
			mutationCalls := 0
			mux := http.NewServeMux()
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.URL.EscapedPath(), "/repos/test-org/test-repo/git/ref/") && !strings.Contains(r.URL.EscapedPath(), "/repos/test-org/test-repo/git/refs/") {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				switch r.Method {
				case http.MethodGet:
					w.Header().Set("ETag", liveETag)
					require.NoError(t, json.NewEncoder(w).Encode(gh.Reference{Object: &gh.GitObject{SHA: gh.Ptr(liveSHA)}}))
					liveSHA = "advanced"
					liveETag = `"advanced-etag"`
				case http.MethodPatch, http.MethodDelete:
					mutationCalls++
					assert.Equal(t, `"old-etag"`, r.Header.Get("If-Match"))
					if r.Header.Get("If-Match") != liveETag {
						w.WriteHeader(http.StatusPreconditionFailed)
						return
					}
					if r.Method == http.MethodPatch {
						liveSHA = "new"
					} else {
						liveSHA = ""
					}
					w.WriteHeader(http.StatusOK)
				default:
					w.WriteHeader(http.StatusMethodNotAllowed)
				}
			})
			client, _ := newTestClient(t, mux)

			err := tt.mutate(context.Background(), client.Repository())

			require.Error(t, err)
			assert.ErrorIs(t, err, platform.ErrRefUpdateConflict)
			assert.Equal(t, 1, mutationCalls)
			assert.Equal(t, "advanced", liveSHA)
		})
	}
}
