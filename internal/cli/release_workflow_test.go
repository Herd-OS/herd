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

func TestReleaseWorkflow_PublishesAllFlavors(t *testing.T) {
	doc := readReleaseWorkflow(t)

	jobs, ok := doc["jobs"].(map[string]any)
	require.True(t, ok, "workflow should have a jobs map")

	job, ok := jobs["publish-flavor-images"].(map[string]any)
	require.True(t, ok, "workflow should define the publish-flavor-images job")

	require.Equal(t, "publish-base-image", job["needs"], "flavor job must run after publish-base-image")

	strategy, ok := job["strategy"].(map[string]any)
	require.True(t, ok, "flavor job should have a strategy")
	matrix, ok := strategy["matrix"].(map[string]any)
	require.True(t, ok, "flavor job strategy should have a matrix")
	flavors, ok := matrix["flavor"].([]any)
	require.True(t, ok, "matrix should list flavors")
	require.Equal(t, []any{"node", "ruby", "python", "go"}, flavors)

	data, err := os.ReadFile("../../.github/workflows/release.yml")
	require.NoError(t, err)
	s := string(data)
	require.Contains(t, s, "publish-flavor-images:")
	require.Contains(t, s, "needs: publish-base-image")
	require.Contains(t, s, "flavor: [node, ruby, python, go]")
	require.Contains(t, s, "--build-arg BASE_VERSION=${VERSION}")
	require.Contains(t, s, "--platform linux/amd64,linux/arm64")
	require.Contains(t, s, "-f images/flavors/${{ matrix.flavor }}/Dockerfile")
	require.Contains(t, s, "ghcr.io/herd-os/herd-runner-${{ matrix.flavor }}:${VERSION}")
	require.Contains(t, s, "ghcr.io/herd-os/herd-runner-${{ matrix.flavor }}:latest")
}
