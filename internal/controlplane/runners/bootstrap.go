package runners

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/controlplane/store"
)

const (
	defaultBootstrapTokenTTL = 24 * time.Hour
	bootstrapTokenBytes      = 32
	bootstrapTokenPrefix     = "hrb_"
)

func GenerateBootstrapToken() (string, error) {
	raw := make([]byte, bootstrapTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate runner bootstrap token: %w", err)
	}
	return bootstrapTokenPrefix + hex.EncodeToString(raw), nil
}

func HashBootstrapToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func NewBootstrapTokenRecord(repositoryID int64, now time.Time, ttl time.Duration) (string, store.RunnerBootstrapToken, error) {
	if repositoryID <= 0 {
		return "", store.RunnerBootstrapToken{}, fmt.Errorf("repository ID is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if ttl <= 0 {
		ttl = defaultBootstrapTokenTTL
	}
	token, err := GenerateBootstrapToken()
	if err != nil {
		return "", store.RunnerBootstrapToken{}, err
	}
	return token, store.RunnerBootstrapToken{
		RepositoryID: repositoryID,
		TokenHash:    HashBootstrapToken(token),
		CreatedAt:    now,
		ExpiresAt:    now.Add(ttl),
	}, nil
}
