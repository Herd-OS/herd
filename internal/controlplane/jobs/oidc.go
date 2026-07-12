package jobs

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/herd-os/herd/internal/controlplane"
	"github.com/herd-os/herd/internal/controlplane/store"
)

const (
	GitHubActionsIssuer = "https://token.actions.githubusercontent.com"
	githubActionsJWKS   = "https://token.actions.githubusercontent.com/.well-known/jwks"
)

var (
	ErrMissingBearerToken = errors.New("missing bearer token")
)

type OIDCClaims struct {
	Issuer      string
	Audience    []string
	Repository  string
	Ref         string
	Workflow    string
	WorkflowRef string
	RunID       string
	ExpiresAt   time.Time
}

type OIDCValidator interface {
	Validate(ctx context.Context, token string) (OIDCClaims, error)
}

type OIDCOptions struct {
	Audience string
	Now      func() time.Time
}

type ExpectedOIDCIdentity struct {
	Repository string
	Ref        string
	Workflow   string
	RunID      string
}

func BearerToken(header string) (string, error) {
	parts := strings.Fields(strings.TrimSpace(header))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", ErrMissingBearerToken
	}
	return parts[1], nil
}

func ValidateOIDCClaims(claims OIDCClaims, expected ExpectedOIDCIdentity, opts OIDCOptions) error {
	audience := strings.TrimSpace(opts.Audience)
	if audience == "" {
		audience = controlplane.DefaultOIDCAudience
	}
	now := time.Now().UTC
	if opts.Now != nil {
		now = opts.Now
	}

	if claims.Issuer != GitHubActionsIssuer {
		return fmt.Errorf("invalid OIDC issuer %q", claims.Issuer)
	}
	if !containsString(claims.Audience, audience) {
		return fmt.Errorf("invalid OIDC audience")
	}
	if strings.TrimSpace(claims.Repository) == "" || claims.Repository != expected.Repository {
		return fmt.Errorf("invalid OIDC repository")
	}
	if !claims.ExpiresAt.After(now()) {
		return fmt.Errorf("OIDC token has expired")
	}
	expectedRef := normalizeExpectedRef(expected.Ref)
	if expectedRef != "" && claims.Ref != expectedRef {
		return fmt.Errorf("invalid OIDC ref")
	}
	if expected.Workflow != "" && !workflowMatches(claims, expected.Workflow) {
		return fmt.Errorf("invalid OIDC workflow")
	}
	if expected.RunID != "" && claims.RunID != expected.RunID {
		return fmt.Errorf("invalid OIDC run ID")
	}
	return nil
}

func ExpectedIdentityFromJob(job store.Job, repository string) ExpectedOIDCIdentity {
	expected := ExpectedOIDCIdentity{}
	var metadata map[string]any
	if len(job.Metadata) == 0 || json.Unmarshal(job.Metadata, &metadata) != nil {
		return expected
	}
	expected.Ref = normalizeExpectedRef(firstMetadataString(metadata, "github_ref", "ref"))
	expected.Workflow = firstMetadataString(metadata, "workflow", "workflow_file", "workflow_ref")
	expected.RunID = firstMetadataString(metadata, "run_id", "workflow_run_id", "github_run_id")
	expected.Repository = firstMetadataString(metadata, "repository")
	if expected.Repository == "" {
		owner := firstMetadataString(metadata, "owner")
		name := firstMetadataString(metadata, "repo", "name")
		if owner != "" && name != "" {
			expected.Repository = owner + "/" + name
		}
	}
	if expected.Repository == "" {
		expected.Repository = repository
	}
	return expected
}

func normalizeExpectedRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "refs/") {
		return ref
	}
	return "refs/heads/" + ref
}

type JWKSValidator struct {
	Audience string
	JWKSURL  string
	Client   *http.Client
	Now      func() time.Time

	mu        sync.Mutex
	cachedAt  time.Time
	cachedKey map[string]*rsa.PublicKey
}

func NewJWKSValidator(audience string) *JWKSValidator {
	return &JWKSValidator{
		Audience: audience,
		JWKSURL:  githubActionsJWKS,
		Client:   http.DefaultClient,
		Now:      func() time.Time { return time.Now().UTC() },
	}
}

func (v *JWKSValidator) Validate(ctx context.Context, token string) (OIDCClaims, error) {
	header, payload, signature, signed, err := splitJWT(token)
	if err != nil {
		return OIDCClaims{}, err
	}
	if header.Algorithm != "RS256" {
		return OIDCClaims{}, fmt.Errorf("unsupported JWT algorithm %q", header.Algorithm)
	}
	key, err := v.key(ctx, header.KeyID)
	if err != nil {
		return OIDCClaims{}, err
	}
	digest := sha256.Sum256([]byte(signed))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return OIDCClaims{}, fmt.Errorf("verify OIDC JWT signature: %w", err)
	}
	claims, err := parseClaims(payload)
	if err != nil {
		return OIDCClaims{}, err
	}
	if err := ValidateOIDCClaims(claims, ExpectedOIDCIdentity{Repository: claims.Repository}, OIDCOptions{
		Audience: v.Audience,
		Now:      v.Now,
	}); err != nil {
		return OIDCClaims{}, err
	}
	return claims, nil
}

func (v *JWKSValidator) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if kid == "" {
		return nil, fmt.Errorf("JWT kid is required")
	}
	v.mu.Lock()
	if key := v.cachedKey[kid]; key != nil && time.Since(v.cachedAt) < 5*time.Minute {
		v.mu.Unlock()
		return key, nil
	}
	v.mu.Unlock()

	keys, err := v.fetchKeys(ctx)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	v.cachedAt = time.Now()
	v.cachedKey = keys
	key := v.cachedKey[kid]
	v.mu.Unlock()
	if key == nil {
		return nil, fmt.Errorf("OIDC signing key not found")
	}
	return key, nil
}

func (v *JWKSValidator) fetchKeys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	client := v.Client
	if client == nil {
		client = http.DefaultClient
	}
	url := strings.TrimSpace(v.JWKSURL)
	if url == "" {
		url = githubActionsJWKS
	}
	//nolint:gosec // JWKSURL is controlled by service config; tests override it with httptest.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	//nolint:gosec // JWKSURL is controlled by service config; tests override it with httptest.
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch OIDC JWKS: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch OIDC JWKS: status %d", resp.StatusCode)
	}
	var set struct {
		Keys []struct {
			KeyID     string `json:"kid"`
			KeyType   string `json:"kty"`
			Algorithm string `json:"alg"`
			Use       string `json:"use"`
			Modulus   string `json:"n"`
			Exponent  string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return nil, fmt.Errorf("decode OIDC JWKS: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, jwk := range set.Keys {
		if jwk.KeyType != "RSA" || jwk.KeyID == "" || jwk.Modulus == "" || jwk.Exponent == "" {
			continue
		}
		key, err := rsaKey(jwk.Modulus, jwk.Exponent)
		if err != nil {
			return nil, err
		}
		keys[jwk.KeyID] = key
	}
	return keys, nil
}

type jwtHeader struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
}

func splitJWT(token string) (jwtHeader, []byte, []byte, string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return jwtHeader{}, nil, nil, "", fmt.Errorf("malformed JWT")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return jwtHeader{}, nil, nil, "", fmt.Errorf("decode JWT header: %w", err)
	}
	var header jwtHeader
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return jwtHeader{}, nil, nil, "", fmt.Errorf("decode JWT header: %w", err)
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return jwtHeader{}, nil, nil, "", fmt.Errorf("decode JWT payload: %w", err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return jwtHeader{}, nil, nil, "", fmt.Errorf("decode JWT signature: %w", err)
	}
	return header, payloadJSON, signature, parts[0] + "." + parts[1], nil
}

func parseClaims(payload []byte) (OIDCClaims, error) {
	var raw struct {
		Issuer      string          `json:"iss"`
		Audience    json.RawMessage `json:"aud"`
		Repository  string          `json:"repository"`
		Ref         string          `json:"ref"`
		Workflow    string          `json:"workflow"`
		WorkflowRef string          `json:"workflow_ref"`
		RunID       string          `json:"run_id"`
		ExpiresAt   int64           `json:"exp"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return OIDCClaims{}, fmt.Errorf("decode JWT claims: %w", err)
	}
	audience, err := parseAudience(raw.Audience)
	if err != nil {
		return OIDCClaims{}, err
	}
	return OIDCClaims{
		Issuer:      raw.Issuer,
		Audience:    audience,
		Repository:  raw.Repository,
		Ref:         raw.Ref,
		Workflow:    raw.Workflow,
		WorkflowRef: raw.WorkflowRef,
		RunID:       raw.RunID,
		ExpiresAt:   time.Unix(raw.ExpiresAt, 0).UTC(),
	}, nil
}

func parseAudience(raw json.RawMessage) ([]string, error) {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}, nil
	}
	var multiple []string
	if err := json.Unmarshal(raw, &multiple); err != nil {
		return nil, fmt.Errorf("decode JWT audience: %w", err)
	}
	return multiple, nil
}

func rsaKey(modulus string, exponent string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(modulus)
	if err != nil {
		return nil, fmt.Errorf("decode JWK modulus: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(exponent)
	if err != nil {
		return nil, fmt.Errorf("decode JWK exponent: %w", err)
	}
	e := 0
	for _, b := range eBytes {
		e = e<<8 + int(b)
	}
	if e == 0 {
		parsed, err := strconv.Atoi(string(eBytes))
		if err != nil {
			return nil, fmt.Errorf("decode JWK exponent")
		}
		e = parsed
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}, nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func workflowMatches(claims OIDCClaims, expected string) bool {
	expected = strings.TrimSpace(expected)
	expectedFile := strings.TrimPrefix(expected, ".github/workflows/")
	if claims.Workflow == expected || claims.Workflow == expectedFile {
		return true
	}
	if claims.WorkflowRef == expected {
		return true
	}
	refFile, ok := workflowFileFromRef(claims.WorkflowRef)
	return ok && (refFile == expected || refFile == expectedFile)
}

func workflowFileFromRef(workflowRef string) (string, bool) {
	beforeRef, _, ok := strings.Cut(workflowRef, "@")
	if !ok {
		return "", false
	}
	_, file, ok := strings.Cut(beforeRef, "/.github/workflows/")
	if !ok || file == "" || strings.Contains(file, "/") {
		return "", false
	}
	return file, true
}

func firstMetadataString(metadata map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := metadata[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				return typed
			}
		case float64:
			if typed > 0 {
				return strconv.FormatInt(int64(typed), 10)
			}
		}
	}
	return ""
}
