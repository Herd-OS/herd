package cli

import (
	"fmt"
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
	require.NoError(t, ensureGitignore(dir, ".herd/state/"))

	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, ".herd/state/\n", string(content))
}

func TestEnsureGitignoreAppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("bin/\n"), 0644))
	require.NoError(t, ensureGitignore(dir, ".herd/state/"))

	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "bin/\n.herd/state/\n", string(content))
}

func TestEnsureGitignoreIdempotent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("bin/\n.herd/state/\n"), 0644))
	require.NoError(t, ensureGitignore(dir, ".herd/state/"))

	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "bin/\n.herd/state/\n", string(content))
}

func TestEnsureGitignoreNoTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("bin/"), 0644))
	require.NoError(t, ensureGitignore(dir, ".herd/state/"))

	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "bin/\n.herd/state/\n", string(content))
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

func TestCreateRoleInstructionFiles(t *testing.T) {
	dir := t.TempDir()
	herdDir := filepath.Join(dir, ".herd")
	require.NoError(t, os.MkdirAll(herdDir, 0755))

	require.NoError(t, createRoleInstructionFiles(herdDir))

	for _, name := range RoleInstructionFiles() {
		path := filepath.Join(herdDir, name)
		info, err := os.Stat(path)
		require.NoError(t, err, "%s should exist", name)
		assert.Equal(t, int64(0), info.Size(), "%s should be empty", name)
	}
}

func TestCreateRoleInstructionFilesDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	herdDir := filepath.Join(dir, ".herd")
	require.NoError(t, os.MkdirAll(herdDir, 0755))

	// Write custom content to planner.md
	custom := []byte("Always write thorough tests.")
	require.NoError(t, os.WriteFile(filepath.Join(herdDir, "planner.md"), custom, 0644))

	require.NoError(t, createRoleInstructionFiles(herdDir))

	// planner.md should keep custom content
	content, err := os.ReadFile(filepath.Join(herdDir, "planner.md"))
	require.NoError(t, err)
	assert.Equal(t, custom, content)

	// Other files should still be created
	for _, name := range []string{"worker.md", "integrator.md"} {
		_, err := os.Stat(filepath.Join(herdDir, name))
		assert.NoError(t, err, "%s should exist", name)
	}
}

func TestRoleInstructionFiles(t *testing.T) {
	files := RoleInstructionFiles()
	assert.Len(t, files, 3)
	assert.Contains(t, files, "planner.md")
	assert.Contains(t, files, "worker.md")
	assert.Contains(t, files, "integrator.md")
}

func TestCreateRunnerFiles(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, createRunnerFiles(dir, "my-org", "my-project"))

	// Dockerfile.runner
	df, err := os.ReadFile(filepath.Join(dir, "Dockerfile.runner"))
	require.NoError(t, err)
	assert.Contains(t, string(df), "FROM ubuntu:24.04")
	assert.Contains(t, string(df), "go install github.com/herd-os/herd/cmd/herd@latest")
	assert.Contains(t, string(df), "ENTRYPOINT")

	// entrypoint.sh
	ep, err := os.ReadFile(filepath.Join(dir, "entrypoint.sh"))
	require.NoError(t, err)
	assert.Contains(t, string(ep), "#!/bin/bash")
	assert.Contains(t, string(ep), "--ephemeral")
	assert.Contains(t, string(ep), "trap cleanup SIGTERM SIGINT")
	assert.Contains(t, string(ep), "exec ./run.sh")
	info, err := os.Stat(filepath.Join(dir, "entrypoint.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm(), "entrypoint.sh should be executable")

	// docker-compose.herd.yml
	dc, err := os.ReadFile(filepath.Join(dir, "docker-compose.herd.yml"))
	require.NoError(t, err)
	assert.Contains(t, string(dc), "REPO_URL=https://github.com/my-org/my-project")
	assert.Contains(t, string(dc), "Dockerfile.runner")
	assert.Contains(t, string(dc), "GITHUB_TOKEN=${GITHUB_TOKEN}")
	assert.Contains(t, string(dc), "CLAUDE_CODE_OAUTH_TOKEN=${CLAUDE_CODE_OAUTH_TOKEN:-}")
	assert.Contains(t, string(dc), "ANTHROPIC_API_KEY=${ANTHROPIC_API_KEY:-}")

	// .env.example
	env, err := os.ReadFile(filepath.Join(dir, ".env.example"))
	require.NoError(t, err)
	assert.Contains(t, string(env), "GITHUB_TOKEN=")
	assert.Contains(t, string(env), "CLAUDE_CODE_OAUTH_TOKEN=")
	assert.Contains(t, string(env), "ANTHROPIC_API_KEY=")
}

func TestCreateRunnerFiles_SkipsExisting(t *testing.T) {
	dir := t.TempDir()

	// Pre-create files with custom content
	custom := []byte("custom content")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile.runner"), custom, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "entrypoint.sh"), custom, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docker-compose.herd.yml"), custom, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env.example"), custom, 0644))

	require.NoError(t, createRunnerFiles(dir, "org", "repo"))

	// All files should keep their custom content
	for _, name := range []string{"Dockerfile.runner", "entrypoint.sh", "docker-compose.herd.yml", ".env.example"} {
		content, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		assert.Equal(t, custom, content, "%s should not be overwritten", name)
	}
}

func TestCreateRunnerFiles_OwnerRepoSubstitution(t *testing.T) {
	tests := []struct {
		name  string
		owner string
		repo  string
	}{
		{"simple", "acme", "app"},
		{"hyphens", "my-org", "my-project"},
		{"underscores", "my_org", "my_project"},
		{"mixed", "Herd-OS", "herd_app"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, createRunnerFiles(dir, tt.owner, tt.repo))

			dc, err := os.ReadFile(filepath.Join(dir, "docker-compose.herd.yml"))
			require.NoError(t, err)
			expected := fmt.Sprintf("REPO_URL=https://github.com/%s/%s", tt.owner, tt.repo)
			assert.Contains(t, string(dc), expected)
		})
	}
}

func TestEnvFileGitignored(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, ensureGitignore(dir, ".env"))

	content, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(content), ".env")

	// Idempotent
	require.NoError(t, ensureGitignore(dir, ".env"))
	content2, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, string(content), string(content2))
}

func TestRenderDockerCompose(t *testing.T) {
	rendered, err := renderDockerCompose("test-org", "test-repo")
	require.NoError(t, err)
	assert.Contains(t, rendered, "https://github.com/test-org/test-repo")
	assert.Contains(t, rendered, "docker compose -f docker-compose.herd.yml")
	assert.Contains(t, rendered, "GITHUB_TOKEN=${GITHUB_TOKEN}")
	assert.Contains(t, rendered, "CLAUDE_CODE_OAUTH_TOKEN=${CLAUDE_CODE_OAUTH_TOKEN:-}")
	assert.Contains(t, rendered, "ANTHROPIC_API_KEY=${ANTHROPIC_API_KEY:-}")
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
