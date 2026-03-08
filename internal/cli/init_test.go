package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectOwnerRepoSSH(t *testing.T) {
	tests := []struct {
		name          string
		remote        string
		expectedOwner string
		expectedRepo  string
	}{
		{"standard SSH", "git@github.com:my-org/my-repo.git", "my-org", "my-repo"},
		{"SSH without .git", "git@github.com:my-org/my-repo", "my-org", "my-repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := setupTestGitRepo(t, tt.remote)
			owner, repo, err := detectOwnerRepo(dir)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedOwner, owner)
			assert.Equal(t, tt.expectedRepo, repo)
		})
	}
}

func TestDetectOwnerRepoHTTPS(t *testing.T) {
	tests := []struct {
		name          string
		remote        string
		expectedOwner string
		expectedRepo  string
	}{
		{"standard HTTPS", "https://github.com/my-org/my-repo.git", "my-org", "my-repo"},
		{"HTTPS without .git", "https://github.com/my-org/my-repo", "my-org", "my-repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := setupTestGitRepo(t, tt.remote)
			owner, repo, err := detectOwnerRepo(dir)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedOwner, owner)
			assert.Equal(t, tt.expectedRepo, repo)
		})
	}
}

func TestDetectOwnerRepoInvalidRemote(t *testing.T) {
	dir := setupTestGitRepo(t, "https://gitlab.com/org/repo.git")
	_, _, err := detectOwnerRepo(dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot parse owner/repo")
}

func TestEnsureGitignoreCreatesFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, ensureGitignore(dir, ".herd/"))

	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, ".herd/\n", string(content))
}

func TestEnsureGitignoreAppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("bin/\n"), 0644))
	require.NoError(t, ensureGitignore(dir, ".herd/"))

	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "bin/\n.herd/\n", string(content))
}

func TestEnsureGitignoreIdempotent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("bin/\n.herd/\n"), 0644))
	require.NoError(t, ensureGitignore(dir, ".herd/"))

	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "bin/\n.herd/\n", string(content))
}

func TestEnsureGitignoreNoTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("bin/"), 0644))
	require.NoError(t, ensureGitignore(dir, ".herd/"))

	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "bin/\n.herd/\n", string(content))
}

func TestInstallWorkflows(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, installWorkflows(dir))

	for _, name := range WorkflowFiles() {
		path := filepath.Join(dir, ".github", "workflows", name)
		_, err := os.Stat(path)
		assert.NoError(t, err, "workflow %s should exist", name)

		content, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.True(t, len(content) > 0, "workflow %s should not be empty", name)
	}
}

func TestInstallWorkflowsIdempotent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, installWorkflows(dir))

	// Get content of first workflow
	first := filepath.Join(dir, ".github", "workflows", WorkflowFiles()[0])
	content1, err := os.ReadFile(first)
	require.NoError(t, err)

	// Run again
	require.NoError(t, installWorkflows(dir))

	content2, err := os.ReadFile(first)
	require.NoError(t, err)
	assert.Equal(t, content1, content2)
}

func TestCheckPrerequisitesNoGitDir(t *testing.T) {
	dir := t.TempDir()
	err := checkPrerequisites(dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a git repository")
}

func TestWorkflowFiles(t *testing.T) {
	files := WorkflowFiles()
	assert.Len(t, files, 3)
	assert.Contains(t, files, "herd-worker.yml")
	assert.Contains(t, files, "herd-monitor.yml")
	assert.Contains(t, files, "herd-integrator.yml")
}

// setupTestGitRepo creates a temp git repo with the given remote URL.
func setupTestGitRepo(t *testing.T, remoteURL string) string {
	t.Helper()
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init"},
		{"git", "remote", "add", "origin", remoteURL},
	}
	for _, args := range cmds {
		cmd := runGit(t, dir, args...)
		require.NoError(t, cmd)
	}
	return dir
}

func runGit(t *testing.T, dir string, args ...string) error {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	return cmd.Run()
}
