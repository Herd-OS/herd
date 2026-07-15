package github

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveTokenFailsClosedInProductionRunner(t *testing.T) {
	for _, key := range []string{"HERD_RUNNER", "HERD_LOCAL_GITHUB_AUTH", "GITHUB_TOKEN", "GH_TOKEN"} {
		t.Setenv(key, "")
	}
	t.Setenv("HERD_RUNNER", "true")

	token, err := resolveToken()

	require.Error(t, err)
	assert.Empty(t, token)
	assert.Contains(t, err.Error(), "local auth is disabled")
	assert.Contains(t, err.Error(), "HERD_RUNNER=true")
}

func TestResolveTokenAllowsExplicitLocalOverrideInRunner(t *testing.T) {
	for _, key := range []string{"HERD_RUNNER", "HERD_LOCAL_GITHUB_AUTH", "GITHUB_TOKEN", "GH_TOKEN"} {
		t.Setenv(key, "")
	}
	t.Setenv("HERD_RUNNER", "true")
	t.Setenv("HERD_LOCAL_GITHUB_AUTH", "true")
	t.Setenv("GITHUB_TOKEN", "ghp_local")

	token, err := resolveToken()

	require.NoError(t, err)
	assert.Equal(t, "ghp_local", token)
}
