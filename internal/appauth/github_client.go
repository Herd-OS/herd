package appauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/google/go-github/v68/github"
	"golang.org/x/oauth2"
)

const (
	defaultMaxRetries       = 3
	githubAppJWTLifetime    = 9 * time.Minute
	githubAppJWTLeeway      = 30 * time.Second
	installationTokenLeeway = 1 * time.Minute
)

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

type jwtGenerator func(time.Time, AppConfig) (string, error)

// NewGitHubTokenSource creates a token source authenticated as a GitHub App.
func NewGitHubTokenSource(cfg AppConfig) (*GitHubTokenSource, *http.Client, error) {
	ts := newGitHubAppJWTTokenSource(cfg, time.Now, GenerateJWT)
	if _, err := ts.Token(); err != nil {
		return nil, nil, err
	}

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

	ts := newInstallationOAuthTokenSource(ctx, source, installationID, time.Now)
	token, err := ts.Token()
	if err != nil {
		return nil, nil, fmt.Errorf("getting GitHub App installation token for installation %d: %w", installationID, err)
	}
	if token.AccessToken == "" {
		return nil, nil, fmt.Errorf("getting GitHub App installation token for installation %d: empty token", installationID)
	}
	httpClient := oauth2.NewClient(ctx, ts)
	httpClient.Transport = newRetryTransport(httpClient.Transport, time.Second)

	return github.NewClient(httpClient), httpClient, nil
}

type installationOAuthTokenSource struct {
	mu             sync.Mutex
	ctx            context.Context
	source         TokenSource
	installationID int64
	now            func() time.Time
	cached         *oauth2.Token
}

func newInstallationOAuthTokenSource(ctx context.Context, source TokenSource, installationID int64, now func() time.Time) *installationOAuthTokenSource {
	if ctx == nil {
		ctx = context.Background()
	}
	if now == nil {
		now = time.Now
	}
	return &installationOAuthTokenSource{
		ctx:            ctx,
		source:         source,
		installationID: installationID,
		now:            now,
	}
}

func (s *installationOAuthTokenSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if s.cached != nil && now.Before(s.cached.Expiry.Add(-installationTokenLeeway)) {
		token := *s.cached
		return &token, nil
	}

	token, err := s.source.InstallationToken(s.ctx, s.installationID)
	if err != nil {
		return nil, err
	}
	if token.Token == "" {
		return nil, fmt.Errorf("empty token")
	}
	cached := &oauth2.Token{
		AccessToken: token.Token,
		TokenType:   "Bearer",
		Expiry:      token.ExpiresAt,
	}
	s.cached = cached
	tokenCopy := *cached
	return &tokenCopy, nil
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
		attemptReq := req.Clone(req.Context())
		if attempt > 0 {
			if !canReplayRequestBody(req) {
				return resp, nil
			}
			if req.Body != nil && req.Body != http.NoBody {
				body, err := req.GetBody()
				if err != nil {
					return nil, fmt.Errorf("retry: failed to rewind request body: %w", err)
				}
				attemptReq.Body = body
			}
		} else {
			attemptReq.Body = req.Body
		}

		resp, err = t.base.RoundTrip(attemptReq)
		if err != nil {
			return nil, err
		}
		if !retryableStatusCodes[resp.StatusCode] {
			return resp, nil
		}
		if attempt < t.maxRetries {
			if !canReplayRequestBody(req) {
				return resp, nil
			}
			io.Copy(io.Discard, resp.Body) //nolint:errcheck // best-effort drain
			_ = resp.Body.Close()
			delay := t.baseDelay * time.Duration(math.Pow(2, float64(attempt)))
			select {
			case <-attemptReq.Context().Done():
				return nil, attemptReq.Context().Err()
			case <-time.After(delay):
			}
		}
	}
	return resp, nil
}

func canReplayRequestBody(req *http.Request) bool {
	return req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
}

type githubAppJWTTokenSource struct {
	mu       sync.Mutex
	cfg      AppConfig
	now      func() time.Time
	generate jwtGenerator
	cached   *oauth2.Token
}

func newGitHubAppJWTTokenSource(cfg AppConfig, now func() time.Time, generate jwtGenerator) *githubAppJWTTokenSource {
	if now == nil {
		now = time.Now
	}
	if generate == nil {
		generate = GenerateJWT
	}
	return &githubAppJWTTokenSource{
		cfg:      cfg,
		now:      now,
		generate: generate,
	}
}

func (s *githubAppJWTTokenSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if s.cached != nil && now.Before(s.cached.Expiry) {
		token := *s.cached
		return &token, nil
	}

	jwt, err := s.generate(now, s.cfg)
	if err != nil {
		return nil, err
	}

	token := &oauth2.Token{
		AccessToken: jwt,
		TokenType:   "Bearer",
		Expiry:      now.Add(githubAppJWTLifetime - githubAppJWTLeeway),
	}
	s.cached = token

	tokenCopy := *token
	return &tokenCopy, nil
}
