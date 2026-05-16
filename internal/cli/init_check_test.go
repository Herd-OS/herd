package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestRunInitCheck_VersionWarningDoesNotBlock proves the version check inserted
// at the top of runInitCheck does not block on an unreachable network endpoint.
// runInitCheck is run in a non-git temp dir so checkPrerequisites fails fast
// after the version check call site executes; the assertion is on elapsed time,
// not on the specific error returned.
func TestRunInitCheck_VersionWarningDoesNotBlock(t *testing.T) {
	setLatestReleaseURL(t, "http://127.0.0.1:1")
	setVersionCheckTimeout(t, 100*time.Millisecond)
	setVersionForTest(t, "v0.5.3")

	dir := t.TempDir()
	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- runInitCheck()
	}()

	select {
	case <-done:
		elapsed := time.Since(start)
		assert.Less(t, elapsed, 2*time.Second,
			"runInitCheck should not block past the version-check timeout when the endpoint is unreachable")
	case <-time.After(2 * time.Second):
		t.Fatal("runInitCheck blocked past 2s — version check is not respecting its timeout")
	}
}

// makeInSyncFixture creates a temp dir representing a herd-initialized repo with
// all managed files matching the embedded templates. Returns the dir path.
func makeInSyncFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%v: %s", args, out)
	}
	run("git", "init")
	run("git", "remote", "add", "origin", "https://github.com/herd-os/herd-test.git")

	cfg := config.Default()
	cfg.Platform.Owner = "herd-os"
	cfg.Platform.Repo = "herd-test"
	cfgBytes, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, config.ConfigFile), cfgBytes, 0o644))

	files, err := renderManagedFiles(dir, "herd-os", "herd-test", cfg)
	require.NoError(t, err)
	for _, mf := range files {
		full := filepath.Join(dir, mf.Path)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, mf.Content, 0o644))
	}

	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile.herd_runner"), []byte("# fixture\n"), 0o644))

	return dir
}

func TestCIGuardrail_PassesWhenInSync(t *testing.T) {
	dir := makeInSyncFixture(t)
	drifted, err := CheckHerdFilesUpToDate(dir)
	require.NoError(t, err)
	assert.Empty(t, drifted, "expected no drift, got: %+v", drifted)
}

func TestCIGuardrail_FailsWhenDrift(t *testing.T) {
	dir := makeInSyncFixture(t)

	workerPath := filepath.Join(dir, ".github", "workflows", "herd-worker.yml")
	existing, err := os.ReadFile(workerPath)
	require.NoError(t, err)
	modified := make([]byte, 0, len(existing)+len("\n# unexpected direct edit\n"))
	modified = append(modified, existing...)
	modified = append(modified, []byte("\n# unexpected direct edit\n")...)
	require.NoError(t, os.WriteFile(workerPath, modified, 0o644))

	result, err := CheckHerdFilesUpToDate(dir)
	require.NoError(t, err)
	require.NotEmpty(t, result, "expected drift to be reported")

	found := false
	for _, d := range result {
		if strings.Contains(d.Path, "herd-worker.yml") {
			found = true
			assert.Equal(t, "content differs", d.Reason)
		}
	}
	assert.True(t, found, "expected herd-worker.yml in drift list, got: %+v", result)
}

func TestCIGuardrail_PassesWhenBothUpdated(t *testing.T) {
	dir := makeInSyncFixture(t)

	cfg, err := config.Load(dir)
	require.NoError(t, err)
	files, err := renderManagedFiles(dir, cfg.Platform.Owner, cfg.Platform.Repo, cfg)
	require.NoError(t, err)
	for _, mf := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, mf.Path), mf.Content, 0o644))
	}

	drifted, err := CheckHerdFilesUpToDate(dir)
	require.NoError(t, err)
	assert.Empty(t, drifted, "expected no drift after consistent re-render, got: %+v", drifted)
}
