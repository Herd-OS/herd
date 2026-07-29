package runners

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateBootstrapToken(t *testing.T) {
	first, err := GenerateBootstrapToken()
	require.NoError(t, err)
	second, err := GenerateBootstrapToken()
	require.NoError(t, err)

	assert.Len(t, first, len(bootstrapTokenPrefix)+(bootstrapTokenBytes*2))
	assert.Contains(t, first, bootstrapTokenPrefix)
	assert.NotEqual(t, first, second)
	assert.NotEqual(t, HashBootstrapToken(first), first)
	assert.Equal(t, HashBootstrapToken(first), HashBootstrapToken(" "+first+" "))
}

func TestNewBootstrapTokenRecord(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)

	plain, record, err := NewBootstrapTokenRecord(42, now, 2*time.Hour)

	require.NoError(t, err)
	assert.NotEmpty(t, plain)
	assert.Equal(t, int64(42), record.RepositoryID)
	assert.Equal(t, HashBootstrapToken(plain), record.TokenHash)
	assert.NotEqual(t, plain, record.TokenHash)
	assert.Equal(t, now, record.CreatedAt)
	assert.Equal(t, now.Add(2*time.Hour), record.ExpiresAt)
}

func TestNewBootstrapTokenRecordRejectsMissingRepository(t *testing.T) {
	plain, record, err := NewBootstrapTokenRecord(0, time.Now(), time.Hour)

	require.Error(t, err)
	assert.Empty(t, plain)
	assert.Zero(t, record)
}
