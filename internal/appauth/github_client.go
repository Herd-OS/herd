package appauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/google/go-github/v68/github"
	"golang.org/x/oauth2"
)

const defaultMaxRetries = 3

var retryableStatusCodes = map[int]bool{
	500: true,
	502: true,
	503: true,
	504: true,
}

// TokenSource creates installation tokens for GitHub App installations.
type TokenSource interface {
	InstallationToken(ctx context.Context, installationID int64) (InstallationToken, error)
}

// GitHubTokenSource creates installation tokens through the GitHub Apps API.
type GitHubTokenSource struct {
	client *github.Client
}

// NewGitHubTokenSource creates a token source authenticated as a GitHub App.
func NewGitHubTokenSource(cfg AppConfig) (*GitHubTokenSource, *http.Client, error) {
	jwt, err := GenerateJWT(time.Now(), cfg)
	if err != nil {
		return nil, nil, err
	}

	ts := oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: jwt,
		TokenType:   "Bearer",
	})
	httpClient := oauth2.NewClient(context.Background(), ts)
	httpClient.Transport = newRetryTransport(httpClient.Transport, time.Second)

	return &GitHubTokenSource{client: github.NewClient(httpClient)}, httpClient, nil
}

// NewGitHubTokenSourceWithClient creates a token source with a caller-supplied GitHub client.
func NewGitHubTokenSourceWithClient(client *github.Client) (*GitHubTokenSource, error) {
	if client == nil {
		return nil, fmt.Errorf("GitHub client is required")
	}
	return &GitHubTokenSource{client: client}, nil
}

// InstallationToken creates an installation access token for installationID.
func (s *GitHubTokenSource) InstallationToken(ctx context.Context, installationID int64) (InstallationToken, error) {
	if s == nil || s.client == nil {
		return InstallationToken{}, fmt.Errorf("GitHub token source client is required")
	}

	token, _, err := s.client.Apps.CreateInstallationToken(ctx, installationID, nil)
	if err != nil {
		return InstallationToken{}, fmt.Errorf("creating GitHub App installation token for installation %d: %w", installationID, err)
	}
	if token == nil {
		return InstallationToken{}, fmt.Errorf("creating GitHub App installation token for installation %d: empty response", installationID)
	}

	return convertInstallationToken(token), nil
}

// NewInstallationClient returns a GitHub client authenticated with an installation token.
func NewInstallationClient(ctx context.Context, source TokenSource, installationID int64) (*github.Client, *http.Client, error) {
	if source == nil {
		return nil, nil, fmt.Errorf("GitHub App token source is required")
	}

	token, err := source.InstallationToken(ctx, installationID)
	if err != nil {
		return nil, nil, fmt.Errorf("getting GitHub App installation token for installation %d: %w", installationID, err)
	}
	if token.Token == "" {
		return nil, nil, fmt.Errorf("getting GitHub App installation token for installation %d: empty token", installationID)
	}

	ts := oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: token.Token,
		TokenType:   "Bearer",
		Expiry:      token.ExpiresAt,
	})
	httpClient := oauth2.NewClient(ctx, ts)
	httpClient.Transport = newRetryTransport(httpClient.Transport, time.Second)

	return github.NewClient(httpClient), httpClient, nil
}

func convertInstallationToken(token *github.InstallationToken) InstallationToken {
	repos := make([]string, 0, len(token.Repositories))
	for _, repo := range token.Repositories {
		if repo == nil {
			continue
		}
		if name := repo.GetName(); name != "" {
			repos = append(repos, name)
		}
	}

	return InstallationToken{
		Token:        token.GetToken(),
		ExpiresAt:    token.GetExpiresAt().Time,
		Permissions:  permissionsMap(token.Permissions),
		Repositories: repos,
	}
}

func permissionsMap(perms *github.InstallationPermissions) map[string]string {
	if perms == nil {
		return nil
	}

	data, err := json.Marshal(perms)
	if err != nil {
		return nil
	}

	out := map[string]string{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type retryTransport struct {
	base       http.RoundTripper
	maxRetries int
	baseDelay  time.Duration
}

func newRetryTransport(base http.RoundTripper, baseDelay time.Duration) *retryTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &retryTransport{
		base:       base,
		maxRetries: defaultMaxRetries,
		baseDelay:  baseDelay,
	}
}

func canRetryRequest(req *http.Request) bool {
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !canRetryRequest(req) {
		return t.base.RoundTrip(req)
	}

	var resp *http.Response
	var err error

	for attempt := 0; attempt <= t.maxRetries; attempt++ {
		if attempt > 0 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, fmt.Errorf("retry: failed to rewind request body: %w", err)
			}
			req.Body = body
		}

		resp, err = t.base.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		if !retryableStatusCodes[resp.StatusCode] {
			return resp, nil
		}
		if attempt < t.maxRetries {
			io.Copy(io.Discard, resp.Body) //nolint:errcheck // best-effort drain
			_ = resp.Body.Close()
			delay := t.baseDelay * time.Duration(math.Pow(2, float64(attempt)))
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(delay):
			}
		}
	}
	return resp, nil
}
