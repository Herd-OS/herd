package artifacts

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	gh "github.com/google/go-github/v68/github"
	"github.com/herd-os/herd/internal/appauth"
)

type GitHubActionsStore struct {
	TokenSource appauth.TokenSource
	HTTPClient  *http.Client
}

func (s GitHubActionsStore) OpenArtifact(ctx context.Context, name string) (io.ReadCloser, error) {
	if s.TokenSource == nil {
		return nil, fmt.Errorf("GitHub App token source is required")
	}
	artifactName := strings.TrimSpace(name)
	if artifactName == "" {
		return nil, fmt.Errorf("artifact name is required")
	}
	artifactCtx := ArtifactRepositoryFromContext(ctx)
	owner, repo, ok := strings.Cut(strings.TrimSpace(artifactCtx.Repository), "/")
	if !ok || owner == "" || repo == "" {
		return nil, fmt.Errorf("artifact repository context is required")
	}
	if artifactCtx.InstallationID <= 0 {
		return nil, fmt.Errorf("artifact installation context is required")
	}
	client, _, err := appauth.NewInstallationClient(ctx, s.TokenSource, artifactCtx.InstallationID)
	if err != nil {
		return nil, fmt.Errorf("create GitHub installation client: %w", err)
	}
	var artifactID int64
	opts := &gh.ListArtifactsOptions{
		Name: gh.Ptr(artifactName),
		ListOptions: gh.ListOptions{
			PerPage: 100,
		},
	}
	for {
		list, resp, err := client.Actions.ListArtifacts(ctx, owner, repo, opts)
		if err != nil {
			return nil, fmt.Errorf("list GitHub Actions artifacts: %w", err)
		}
		for _, artifact := range list.Artifacts {
			if artifact.GetName() == artifactName && !artifact.GetExpired() {
				artifactID = artifact.GetID()
				break
			}
		}
		if artifactID != 0 || resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	if artifactID == 0 {
		return nil, fmt.Errorf("GitHub Actions artifact %q not found", artifactName)
	}
	downloadURL, _, err := client.Actions.DownloadArtifact(ctx, owner, repo, artifactID, 1)
	if err != nil {
		return nil, fmt.Errorf("download GitHub Actions artifact: %w", err)
	}
	httpClient := s.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch GitHub Actions artifact archive: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() {
			_ = resp.Body.Close()
		}()
		return nil, fmt.Errorf("fetch GitHub Actions artifact archive: %s", resp.Status)
	}
	return resp.Body, nil
}
