package appauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v68/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestGenerateJWTValidClaimsAndHeader(t *testing.T) {
	key, keyPEM := generatedTestRSAKey(t)
	now := time.Date(2026, 7, 11, 12, 30, 0, 0, time.UTC)

	token, err := GenerateJWT(now, AppConfig{AppID: 12345, PrivateKeyPEM: keyPEM})
	require.NoError(t, err)

	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)

	var header map[string]string
	decodeJWTPart(t, parts[0], &header)
	assert.Equal(t, "RS256", header["alg"])
	assert.Equal(t, "JWT", header["typ"])

	var claims map[string]any
	decodeJWTPart(t, parts[1], &claims)
	assert.Equal(t, "12345", claims["iss"])

	iat, ok := claims["iat"].(float64)
	require.True(t, ok)
	exp, ok := claims["exp"].(float64)
	require.True(t, ok)
	assert.Equal(t, float64(now.Add(-time.Minute).Unix()), iat)
	assert.LessOrEqual(t, exp-iat, float64((10 * time.Minute).Seconds()))
	assert.Equal(t, float64((10 * time.Minute).Seconds()), exp-iat)

	signingInput := parts[0] + "." + parts[1]
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	require.NoError(t, err)
	sum := sha256.Sum256([]byte(signingInput))
	require.NoError(t, rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], signature))
}

func TestGenerateJWTErrors(t *testing.T) {
	_, keyPEM := generatedTestRSAKey(t)
	tests := []struct {
		name    string
		cfg     AppConfig
		wantErr string
	}{
		{
			name:    "empty app id",
			cfg:     AppConfig{PrivateKeyPEM: keyPEM},
			wantErr: "GitHub App ID is required",
		},
		{
			name:    "invalid pem",
			cfg:     AppConfig{AppID: 1, PrivateKeyPEM: []byte("not pem")},
			wantErr: "invalid GitHub App private key",
		},
		{
			name:    "empty pem",
			cfg:     AppConfig{AppID: 1},
			wantErr: "invalid GitHub App private key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, err := GenerateJWT(time.Now(), tt.cfg)
			require.Error(t, err)
			assert.Empty(t, token)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestGitHubTokenSourceInstallationTokenSuccess(t *testing.T) {
	expiresAt := time.Date(2026, 7, 11, 13, 0, 0, 0, time.UTC)
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		assert.Equal(t, http.MethodPost, req.Method)
		assert.Equal(t, "/api/v3/app/installations/99/access_tokens", req.URL.Path)
		assert.Equal(t, "Bearer app-jwt", req.Header.Get("Authorization"))

		return jsonResponse(http.StatusCreated, `{
			"token":"installation-token",
			"expires_at":"`+expiresAt.Format(time.RFC3339)+`",
			"permissions":{"issues":"write","metadata":"read"},
			"repositories":[{"name":"herd"},{"name":"infra"}]
		}`), nil
	})
	httpClient := oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: "app-jwt",
		TokenType:   "Bearer",
	}))
	oauthTransport, ok := httpClient.Transport.(*oauth2.Transport)
	require.True(t, ok)
	oauthTransport.Base = rt
	ghClient, err := github.NewClient(httpClient).WithEnterpriseURLs("https://example.test/api/v3/", "https://example.test/api/uploads/")
	require.NoError(t, err)
	source, err := NewGitHubTokenSourceWithClient(ghClient)
	require.NoError(t, err)

	token, err := source.InstallationToken(context.Background(), 99)
	require.NoError(t, err)

	assert.Equal(t, "installation-token", token.Token)
	assert.Equal(t, expiresAt, token.ExpiresAt)
	assert.Equal(t, map[string]string{"issues": "write", "metadata": "read"}, token.Permissions)
	assert.Equal(t, []string{"herd", "infra"}, token.Repositories)
}

func TestGitHubTokenSourceInstallationTokenError(t *testing.T) {
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusInternalServerError, `{"message":"server error"}`), nil
	})
	httpClient := &http.Client{Transport: rt}
	ghClient, err := github.NewClient(httpClient).WithEnterpriseURLs("https://example.test/api/v3/", "https://example.test/api/uploads/")
	require.NoError(t, err)
	source, err := NewGitHubTokenSourceWithClient(ghClient)
	require.NoError(t, err)

	token, err := source.InstallationToken(context.Background(), 42)
	require.Error(t, err)
	assert.Empty(t, token.Token)
	assert.Contains(t, err.Error(), "installation 42")
}

func TestNewInstallationClient(t *testing.T) {
	expiresAt := time.Date(2026, 7, 11, 13, 0, 0, 0, time.UTC)
	source := &fakeTokenSource{
		token: InstallationToken{
			Token:     "installation-token",
			ExpiresAt: expiresAt,
		},
	}

	client, httpClient, err := NewInstallationClient(context.Background(), source, 77)
	require.NoError(t, err)
	require.NotNil(t, client)
	require.NotNil(t, httpClient)
	assert.Equal(t, int64(77), source.installationID)

	capture := &capturingTransport{status: http.StatusOK}
	retry, ok := httpClient.Transport.(*retryTransport)
	require.True(t, ok)
	oauthTransport, ok := retry.base.(*oauth2.Transport)
	require.True(t, ok)
	oauthTransport.Base = capture

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.github.test/repos/herd/herd", nil)
	require.NoError(t, err)
	resp, err := httpClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, "Bearer installation-token", capture.authorization)
}

func TestNewInstallationClientErrorPropagation(t *testing.T) {
	sourceErr := errors.New("token source unavailable")
	source := &fakeTokenSource{err: sourceErr}

	client, httpClient, err := NewInstallationClient(context.Background(), source, 77)
	require.Error(t, err)
	assert.Nil(t, client)
	assert.Nil(t, httpClient)
	assert.ErrorIs(t, err, sourceErr)
	assert.Contains(t, err.Error(), "installation 77")
}

func TestNewInstallationClientRejectsEmptyToken(t *testing.T) {
	source := &fakeTokenSource{}

	client, httpClient, err := NewInstallationClient(context.Background(), source, 77)
	require.Error(t, err)
	assert.Nil(t, client)
	assert.Nil(t, httpClient)
	assert.Contains(t, err.Error(), "empty token")
}

func TestRetryTransportRetriesOnlyReadRequests(t *testing.T) {
	tests := []struct {
		name          string
		method        string
		path          string
		body          string
		nonReplayable bool
		wantBodies    []string
		wantCalls     int
		wantStatus    int
	}{
		{
			name:       "get retries transient server error",
			method:     http.MethodGet,
			path:       "/repos/herd/herd/issues/1",
			body:       "{}",
			wantBodies: []string{"{}", "{}"},
			wantCalls:  2,
			wantStatus: http.StatusOK,
		},
		{
			name:       "get retries transient server error with replayable body",
			method:     http.MethodGet,
			path:       "/repos/herd/herd/search/issues",
			body:       `{"query":"repo:herd/herd"}`,
			wantBodies: []string{`{"query":"repo:herd/herd"}`, `{"query":"repo:herd/herd"}`},
			wantCalls:  2,
			wantStatus: http.StatusOK,
		},
		{
			name:          "get with non replayable body is not retried",
			method:        http.MethodGet,
			path:          "/repos/herd/herd/search/issues",
			body:          `{"query":"repo:herd/herd"}`,
			nonReplayable: true,
			wantBodies:    []string{`{"query":"repo:herd/herd"}`},
			wantCalls:     1,
			wantStatus:    http.StatusInternalServerError,
		},
		{
			name:       "create issue comment post is not retried",
			method:     http.MethodPost,
			path:       "/repos/herd/herd/issues/1/comments",
			body:       "{}",
			wantBodies: []string{"{}"},
			wantCalls:  1,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "create issue post is not retried",
			method:     http.MethodPost,
			path:       "/repos/herd/herd/issues",
			body:       "{}",
			wantBodies: []string{"{}"},
			wantCalls:  1,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "workflow dispatch post is not retried",
			method:     http.MethodPost,
			path:       "/repos/herd/herd/actions/workflows/herd-worker.yml/dispatches",
			body:       "{}",
			wantBodies: []string{"{}"},
			wantCalls:  1,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "put mutation is not retried",
			method:     http.MethodPut,
			path:       "/repos/herd/herd/issues/1/labels/bug",
			body:       "{}",
			wantBodies: []string{"{}"},
			wantCalls:  1,
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			var bodies []string
			transport := newRetryTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				assert.Equal(t, tt.method, req.Method)
				assert.Equal(t, tt.path, req.URL.Path)
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				bodies = append(bodies, string(body))
				if calls == 1 {
					return jsonResponse(http.StatusInternalServerError, `{"message":"server error"}`), nil
				}
				return jsonResponse(http.StatusOK, `{}`), nil
			}), 0)

			req, err := http.NewRequestWithContext(context.Background(), tt.method, "https://api.github.test"+tt.path, strings.NewReader(tt.body))
			require.NoError(t, err)
			if tt.nonReplayable {
				req.Body = io.NopCloser(strings.NewReader(tt.body))
				req.GetBody = nil
			}

			resp, err := transport.RoundTrip(req)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			assert.Equal(t, tt.wantCalls, calls)
			assert.Equal(t, tt.wantStatus, resp.StatusCode)
			assert.Equal(t, tt.wantBodies, bodies)
		})
	}
}

func generatedTestRSAKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	require.NotEmpty(t, keyPEM)
	return key, keyPEM
}

func decodeJWTPart(t *testing.T, part string, target any) {
	t.Helper()

	data, err := base64.RawURLEncoding.DecodeString(part)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, target))
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type fakeTokenSource struct {
	token          InstallationToken
	err            error
	installationID int64
}

func (s *fakeTokenSource) InstallationToken(_ context.Context, installationID int64) (InstallationToken, error) {
	s.installationID = installationID
	if s.err != nil {
		return InstallationToken{}, s.err
	}
	return s.token, nil
}

type capturingTransport struct {
	status        int
	authorization string
}

func (t *capturingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.authorization = req.Header.Get("Authorization")
	return &http.Response{
		StatusCode: t.status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("{}")),
	}, nil
}
