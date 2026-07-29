package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviewWorkerParamsPassesPromptAsExtraInstructions(t *testing.T) {
	tests := []struct {
		name       string
		prompt     string
		wantPrompt string
	}{
		{name: "empty prompt"},
		{name: "trims focused prompt", prompt: "  focus on auth and retries  ", wantPrompt: "focus on auth and retries"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := reviewWorkerParams(849, "/repo", tt.prompt, false)

			assert.Equal(t, 849, params.PRNumber)
			assert.Equal(t, "/repo", params.RepoRoot)
			assert.Equal(t, tt.wantPrompt, params.ExtraInstructions)
			assert.False(t, params.Manual)
		})
	}
}

func TestReviewWorkerParamsSetsManualFlag(t *testing.T) {
	tests := []struct {
		name       string
		manual     bool
		wantManual bool
	}{
		{name: "manual false by default"},
		{name: "manual true", manual: true, wantManual: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := reviewWorkerParams(849, "/repo", "  focus\n\nhere  ", tt.manual)

			assert.Equal(t, "focus\n\nhere", params.ExtraInstructions)
			assert.Equal(t, tt.wantManual, params.Manual)
		})
	}
}

func TestReviewWorkerCommandAcceptsPromptAndManualFlags(t *testing.T) {
	cmd := newReviewWorkerCmd()

	promptFlag := cmd.Flags().Lookup("prompt")
	manualFlag := cmd.Flags().Lookup("manual")

	require.NotNil(t, promptFlag)
	assert.Equal(t, "", promptFlag.DefValue)
	require.NotNil(t, manualFlag)
	assert.Equal(t, "false", manualFlag.DefValue)
}

func TestHostedReviewReadTokenUsesControlPlaneWithoutLegacyGitHubToken(t *testing.T) {
	for _, key := range []string{"GITHUB_TOKEN", "GH_TOKEN", "HERD_GITHUB_TOKEN"} {
		t.Setenv(key, "")
	}
	t.Setenv("HERD_RUNNER", "true")
	t.Setenv("HERD_JOB_ID", "job-1")
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "bearer request-token", r.Header.Get("Authorization"))
		assert.Equal(t, "herd-control-plane", r.URL.Query().Get("audience"))
		_ = json.NewEncoder(w).Encode(map[string]string{"value": "oidc-token"})
	}))
	t.Cleanup(oidc.Close)
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/jobs/job-1/review-read-token", r.URL.Path)
		assert.Equal(t, "Bearer oidc-token", r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "ghs_hosted_read_token",
			"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	t.Cleanup(cp.Close)
	restore := replaceGitHubActionsOIDCHTTPClient(rewriteOIDCClient(t, oidc.URL))
	t.Cleanup(restore)
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://pipelines.actions.githubusercontent.com/token?existing=1")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "request-token")

	token, err := hostedReviewReadToken(t.Context(), &config.Config{ControlPlaneURL: cp.URL})

	require.NoError(t, err)
	assert.Equal(t, "ghs_hosted_read_token", token)
}

func TestHostedReviewReadTokenPrefersValidatedEnvControlPlaneURL(t *testing.T) {
	t.Setenv("HERD_RUNNER", "true")
	t.Setenv("HERD_JOB_ID", "job-1")
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"value": "oidc-token"})
	}))
	t.Cleanup(oidc.Close)
	calledEnvControlPlane := false
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calledEnvControlPlane = true
		assert.Equal(t, "/api/v1/jobs/job-1/review-read-token", r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "ghs_env_read_token",
			"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	t.Cleanup(cp.Close)
	restore := replaceGitHubActionsOIDCHTTPClient(rewriteOIDCClient(t, oidc.URL))
	t.Cleanup(restore)
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://pipelines.actions.githubusercontent.com/token")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "request-token")
	t.Setenv("HERD_CONTROL_PLANE_URL", cp.URL)

	token, err := hostedReviewReadToken(t.Context(), &config.Config{ControlPlaneURL: config.DefaultControlPlaneURL})

	require.NoError(t, err)
	assert.Equal(t, "ghs_env_read_token", token)
	assert.True(t, calledEnvControlPlane)
}

func TestGitHubActionsOIDCTokenRejectsMissingEnvironment(t *testing.T) {
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")

	token, err := githubActionsOIDCToken(t.Context())

	require.Error(t, err)
	assert.Empty(t, token)
	assert.Contains(t, err.Error(), "OIDC request environment")
}

func TestGitHubActionsOIDCTokenRejectsEmptyValue(t *testing.T) {
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"value":""}`))
	}))
	t.Cleanup(oidc.Close)
	restore := replaceGitHubActionsOIDCHTTPClient(rewriteOIDCClient(t, oidc.URL))
	t.Cleanup(restore)
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://pipelines.actions.githubusercontent.com/token")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "request-token")

	token, err := githubActionsOIDCToken(t.Context())

	require.Error(t, err)
	assert.Empty(t, token)
	assert.Contains(t, err.Error(), "did not include a token")
}

func TestGitHubActionsOIDCTokenRejectsUntrustedURLs(t *testing.T) {
	tests := []struct {
		name       string
		requestURL string
		want       string
	}{
		{name: "http", requestURL: "http://pipelines.actions.githubusercontent.com/token", want: "must use https"},
		{name: "unexpected host", requestURL: "https://example.test/token", want: "host is not trusted"},
		{name: "userinfo", requestURL: "https://token@pipelines.actions.githubusercontent.com/token", want: "URL is invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", tt.requestURL)
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "request-token")

			token, err := githubActionsOIDCToken(t.Context())

			require.Error(t, err)
			assert.Empty(t, token)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestValidateGitHubActionsOIDCRequestURLAcceptsActionsHost(t *testing.T) {
	parsed, err := url.Parse("https://pipelines.actions.githubusercontent.com/_apis/oidc/token")
	require.NoError(t, err)

	assert.NoError(t, validateGitHubActionsOIDCRequestURL(parsed))
}

func replaceGitHubActionsOIDCHTTPClient(client oidcHTTPDoer) func() {
	previous := githubActionsOIDCHTTPClient
	githubActionsOIDCHTTPClient = client
	return func() {
		githubActionsOIDCHTTPClient = previous
	}
}

func rewriteOIDCClient(t *testing.T, target string) *http.Client {
	t.Helper()
	targetURL, err := url.Parse(strings.TrimRight(target, "/"))
	require.NoError(t, err)
	return &http.Client{Transport: rewriteOIDCTransport{target: targetURL, base: http.DefaultTransport}}
}

type rewriteOIDCTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (t rewriteOIDCTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rewritten := req.Clone(context.Background())
	rewritten.URL.Scheme = t.target.Scheme
	rewritten.URL.Host = t.target.Host
	return t.base.RoundTrip(rewritten)
}
