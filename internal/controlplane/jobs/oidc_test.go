package jobs

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/controlplane/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateOIDCClaims(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	base := OIDCClaims{
		Issuer:     GitHubActionsIssuer,
		Audience:   []string{"herd-control-plane"},
		Repository: "acme/widgets",
		Ref:        "refs/heads/herd/worker/837",
		Workflow:   "worker.yml",
		RunID:      "12345",
		ExpiresAt:  now.Add(time.Hour),
	}
	expected := ExpectedOIDCIdentity{
		Repository: "acme/widgets",
		Ref:        "refs/heads/herd/worker/837",
		Workflow:   "worker.yml",
		RunID:      "12345",
	}

	tests := []struct {
		name    string
		mutate  func(*OIDCClaims)
		wantErr string
	}{
		{name: "valid claims"},
		{
			name: "wrong issuer",
			mutate: func(claims *OIDCClaims) {
				claims.Issuer = "https://example.com"
			},
			wantErr: "issuer",
		},
		{
			name: "wrong audience",
			mutate: func(claims *OIDCClaims) {
				claims.Audience = []string{"other"}
			},
			wantErr: "audience",
		},
		{
			name: "wrong repository",
			mutate: func(claims *OIDCClaims) {
				claims.Repository = "acme/other"
			},
			wantErr: "repository",
		},
		{
			name: "wrong workflow",
			mutate: func(claims *OIDCClaims) {
				claims.Workflow = "review.yml"
			},
			wantErr: "workflow",
		},
		{
			name: "wrong run ID",
			mutate: func(claims *OIDCClaims) {
				claims.RunID = "999"
			},
			wantErr: "run ID",
		},
		{
			name: "expired token",
			mutate: func(claims *OIDCClaims) {
				claims.ExpiresAt = now.Add(-time.Second)
			},
			wantErr: "expired",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := base
			if tt.mutate != nil {
				tt.mutate(&claims)
			}

			err := ValidateOIDCClaims(claims, expected, OIDCOptions{
				Audience: "herd-control-plane",
				Now:      func() time.Time { return now },
			})

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestBearerToken(t *testing.T) {
	token, err := BearerToken("Bearer abc.def")

	require.NoError(t, err)
	assert.Equal(t, "abc.def", token)

	_, err = BearerToken("")
	require.ErrorIs(t, err, ErrMissingBearerToken)
}

func TestJWKSValidatorValidateWithLocalKey(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{jwkForKey("test-key", &privateKey.PublicKey)},
		})
	}))
	defer server.Close()

	token := signedOIDCToken(t, privateKey, "test-key", map[string]any{
		"iss":        GitHubActionsIssuer,
		"aud":        "herd-control-plane",
		"repository": "acme/widgets",
		"ref":        "refs/heads/herd/worker/837",
		"workflow":   "worker.yml",
		"run_id":     "12345",
		"exp":        now.Add(time.Hour).Unix(),
	})
	validator := &JWKSValidator{
		Audience: "herd-control-plane",
		JWKSURL:  server.URL,
		Client:   server.Client(),
		Now:      func() time.Time { return now },
	}

	claims, err := validator.Validate(context.Background(), token)

	require.NoError(t, err)
	assert.Equal(t, "acme/widgets", claims.Repository)
	assert.Equal(t, "12345", claims.RunID)
}

func TestJWKSValidatorFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer server.Close()
	token := signedOIDCToken(t, mustRSAKey(t), "missing", map[string]any{
		"iss":        GitHubActionsIssuer,
		"aud":        "herd-control-plane",
		"repository": "acme/widgets",
		"exp":        time.Now().Add(time.Hour).Unix(),
	})
	validator := &JWKSValidator{Audience: "herd-control-plane", JWKSURL: server.URL, Client: server.Client()}

	_, err := validator.Validate(context.Background(), token)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "fetch OIDC JWKS")
}

func TestExpectedIdentityFromJob(t *testing.T) {
	metadata := json.RawMessage(`{"ref":"refs/heads/main","workflow_file":"worker.yml","workflow_run_id":123}`)

	expected := ExpectedIdentityFromJob(store.Job{Metadata: metadata}, "acme/widgets")

	assert.Equal(t, ExpectedOIDCIdentity{
		Repository: "acme/widgets",
		Ref:        "refs/heads/main",
		Workflow:   "worker.yml",
		RunID:      "123",
	}, expected)
}

func signedOIDCToken(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header := map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid}
	headerJSON, err := json.Marshal(header)
	require.NoError(t, err)
	claimsJSON, err := json.Marshal(claims)
	require.NoError(t, err)
	signed := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(signed))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	require.NoError(t, err)
	return signed + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func jwkForKey(kid string, key *rsa.PublicKey) map[string]string {
	return map[string]string{
		"kty": "RSA",
		"kid": kid,
		"alg": "RS256",
		"use": "sig",
		"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(bigEndianInt(key.E)),
	}
}

func bigEndianInt(value int) []byte {
	if value == 0 {
		return []byte{0}
	}
	var out []byte
	for value > 0 {
		out = append([]byte{byte(value & 0xff)}, out...)
		value >>= 8
	}
	return out
}

func mustRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return key
}
