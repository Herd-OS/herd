package appauth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// AppConfig contains the credentials needed to authenticate as a GitHub App.
type AppConfig struct {
	AppID         int64
	PrivateKeyPEM []byte
}

// InstallationToken is the app installation token data used by service code.
type InstallationToken struct {
	Token        string
	ExpiresAt    time.Time
	Permissions  map[string]string
	Repositories []string
}

// GenerateJWT creates a GitHub App JWT signed with RS256.
func GenerateJWT(now time.Time, cfg AppConfig) (string, error) {
	if cfg.AppID == 0 {
		return "", fmt.Errorf("GitHub App ID is required")
	}

	privateKey, err := parseRSAPrivateKey(cfg.PrivateKeyPEM)
	if err != nil {
		return "", fmt.Errorf("invalid GitHub App private key: %w", err)
	}

	iat := now.Add(-time.Minute).Unix()
	claims := map[string]any{
		"iat": iat,
		"exp": iat + int64((10 * time.Minute).Seconds()),
		"iss": strconv.FormatInt(cfg.AppID, 10),
	}
	header := map[string]string{
		"alg": "RS256",
		"typ": "JWT",
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("marshaling JWT header: %w", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshaling JWT claims: %w", err)
	}

	encodedHeader := base64.RawURLEncoding.EncodeToString(headerJSON)
	encodedClaims := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := encodedHeader + "." + encodedClaims

	sum := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("signing GitHub App JWT: %w", err)
	}

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func parseRSAPrivateKey(privateKeyPEM []byte) (*rsa.PrivateKey, error) {
	if len(privateKeyPEM) == 0 {
		return nil, errors.New("private key PEM is required")
	}

	block, _ := pem.Decode(privateKeyPEM)
	if block == nil {
		return nil, errors.New("PEM block not found")
	}

	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}

	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key must be RSA, got %T", parsed)
	}
	return key, nil
}
