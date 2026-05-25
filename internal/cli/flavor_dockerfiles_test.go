package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFlavorDockerfiles_FromPinnedBase(t *testing.T) {
	flavors := []string{"node", "ruby", "python", "go"}

	for _, flavor := range flavors {
		t.Run(flavor, func(t *testing.T) {
			path := filepath.Join("..", "..", "images", "flavors", flavor, "Dockerfile")
			data, err := os.ReadFile(path)
			require.NoError(t, err)

			content := string(data)
			assert.Contains(t, content, "ARG BASE_VERSION")
			assert.Contains(t, content, "FROM ghcr.io/herd-os/herd-runner-base:${BASE_VERSION}")
			assert.NotContains(t, content, ":latest")
		})
	}
}
