package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeManifest(t *testing.T, dir, name string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0644))
}

func TestDetectRunnerFlavor_Go(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "go.mod")

	flavor, matched := detectRunnerFlavor(dir)
	assert.Equal(t, "go", flavor)
	assert.Contains(t, matched, "go.mod")
}

func TestDetectRunnerFlavor_Ruby(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "Gemfile")

	flavor, matched := detectRunnerFlavor(dir)
	assert.Equal(t, "ruby", flavor)
	assert.Contains(t, matched, "Gemfile")
}

func TestDetectRunnerFlavor_Node(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "package.json")

	flavor, matched := detectRunnerFlavor(dir)
	assert.Equal(t, "node", flavor)
	assert.Contains(t, matched, "package.json")
}

func TestDetectRunnerFlavor_Python(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
	}{
		{"requirements.txt", "requirements.txt"},
		{"pyproject.toml", "pyproject.toml"},
		{"setup.py", "setup.py"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeManifest(t, dir, tt.manifest)

			flavor, matched := detectRunnerFlavor(dir)
			assert.Equal(t, "python", flavor)
			assert.Contains(t, matched, tt.manifest)
		})
	}
}

func TestDetectRunnerFlavor_Priority(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "go.mod")
	writeManifest(t, dir, "package.json")

	flavor, matched := detectRunnerFlavor(dir)
	assert.Equal(t, "go", flavor)
	assert.GreaterOrEqual(t, len(matched), 2)
	assert.Contains(t, matched, "go.mod")
	assert.Contains(t, matched, "package.json")
}

func TestDetectRunnerFlavor_None(t *testing.T) {
	dir := t.TempDir()

	flavor, matched := detectRunnerFlavor(dir)
	assert.Equal(t, "base", flavor)
	assert.Empty(t, matched)
}

func TestRunnerFlavorOverride_Valid(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "go.mod")

	flavor, err := resolveRunnerFlavor(dir, "ruby")
	require.NoError(t, err)
	assert.Equal(t, "ruby", flavor)
}

func TestRunnerFlavorOverride_Invalid(t *testing.T) {
	dir := t.TempDir()

	_, err := resolveRunnerFlavor(dir, "cobol")
	require.Error(t, err)
	for _, f := range []string{"node", "ruby", "python", "go", "base"} {
		assert.Contains(t, err.Error(), f)
	}
}

func TestRunnerDockerfileTemplate_FlavorInFromLine(t *testing.T) {
	for _, flavor := range []string{"go", "python"} {
		t.Run(flavor, func(t *testing.T) {
			content, err := renderHerdRunnerDockerfile(runnerBaseImage(flavor))
			require.NoError(t, err)

			expected := "FROM ghcr.io/herd-os/herd-runner-" + flavor + ":" + runnerImageTag(version)
			assert.Contains(t, string(content), expected)
		})
	}
}
