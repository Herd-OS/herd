package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestServiceDockerfileRuntimeImage(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile.herd_service"))
	require.NoError(t, err)
	dockerfile := string(data)

	assert.Contains(t, dockerfile, "FROM --platform=$BUILDPLATFORM golang:1.26.1-alpine AS build")
	assert.Contains(t, dockerfile, "go build -trimpath")
	assert.Contains(t, dockerfile, "./cmd/herd-service")
	assert.Contains(t, dockerfile, "FROM gcr.io/distroless/static-debian12:nonroot")
	assert.Contains(t, dockerfile, "COPY --from=build --chown=nonroot:nonroot /out/herd-service /app/herd-service")
	assert.Contains(t, dockerfile, "USER nonroot:nonroot")
	assert.Contains(t, dockerfile, "EXPOSE 8080")

	runtimeStage := dockerfile[strings.LastIndex(dockerfile, "FROM gcr.io/distroless/static-debian12:nonroot"):]
	assert.NotContains(t, runtimeStage, "COPY .")
	assert.NotContains(t, runtimeStage, "HERD_GITHUB_APP_PRIVATE_KEY")
	assert.NotContains(t, runtimeStage, "HERD_WEBHOOK_SECRET")
	assert.NotContains(t, runtimeStage, "HERD_DATABASE_URL")
}

func TestServiceImageWorkflowPublishesExpectedPackage(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "herd-service-image.yml"))
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(data, &doc))

	jobs, ok := doc["jobs"].(map[string]any)
	require.True(t, ok, "workflow should define jobs")
	assert.Contains(t, jobs, "smoke-test")
	assert.Contains(t, jobs, "publish")

	workflow := string(data)
	assert.Contains(t, workflow, "IMAGE: ghcr.io/herd-os/herd-service")
	assert.Contains(t, workflow, "-f Dockerfile.herd_service")
	assert.Contains(t, workflow, "-t ${IMAGE}:${VERSION}")
	assert.Contains(t, workflow, "-t ${IMAGE}:latest")
	assert.Contains(t, workflow, "make image-service-smoke")
	assert.NotContains(t, workflow, "herd-runner-base")
	assert.NotContains(t, workflow, "Dockerfile.herd_runner")
}
