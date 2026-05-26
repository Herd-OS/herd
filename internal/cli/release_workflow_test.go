package cli

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func readReleaseWorkflow(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile("../../.github/workflows/release.yml")
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(data, &doc))
	return doc
}

func TestReleaseWorkflow_PublishesBaseImage(t *testing.T) {
	doc := readReleaseWorkflow(t)

	jobs, ok := doc["jobs"].(map[string]any)
	require.True(t, ok, "workflow should have a jobs map")
	_, ok = jobs["publish-base-image"]
	require.True(t, ok, "workflow should define the publish-base-image job")

	data, err := os.ReadFile("../../.github/workflows/release.yml")
	require.NoError(t, err)
	s := string(data)
	require.Contains(t, s, "publish-base-image:")
	require.Contains(t, s, "ghcr.io/herd-os/herd-runner-base:${VERSION}")
	require.Contains(t, s, "ghcr.io/herd-os/herd-runner-base:latest")
	require.Contains(t, s, "linux/amd64,linux/arm64")
}
