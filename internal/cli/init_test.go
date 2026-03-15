package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	assert.Contains(t, string(df), "/opt/herd/bin")
	assert.Contains(t, string(df), "ENTRYPOINT")

	// entrypoint.herd.sh
	ep, err := os.ReadFile(filepath.Join(dir, "entrypoint.herd.sh"))
	require.NoError(t, err)
	assert.Contains(t, string(ep), "#!/bin/bash")
	assert.Contains(t, string(ep), "--ephemeral")
	assert.Contains(t, string(ep), "trap cleanup SIGTERM SIGINT")
	assert.Contains(t, string(ep), "exec ./run.sh")
	assert.Contains(t, string(ep), "Herd-OS/herd/releases")
	assert.Contains(t, string(ep), "HERD_VERSION")
	info, err := os.Stat(filepath.Join(dir, "entrypoint.herd.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm(), "entrypoint.herd.sh should be executable")

	// docker-compose.herd.yml
	dc, err := os.ReadFile(filepath.Join(dir, "docker-compose.herd.yml"))
	require.NoError(t, err)
	assert.Contains(t, string(dc), "REPO_URL=https://github.com/my-org/my-project")
	assert.Contains(t, string(dc), "Dockerfile.runner")
	assert.Contains(t, string(dc), "GITHUB_TOKEN=${GITHUB_TOKEN}")
	assert.Contains(t, string(dc), "CLAUDE_CODE_OAUTH_TOKEN=${CLAUDE_CODE_OAUTH_TOKEN:-}")
	assert.Contains(t, string(dc), "ANTHROPIC_API_KEY=${ANTHROPIC_API_KEY:-}")

	// .env.herd.example
	env, err := os.ReadFile(filepath.Join(dir, ".env.herd.example"))
	require.NoError(t, err)
	assert.Contains(t, string(env), "GITHUB_TOKEN=")
	assert.Contains(t, string(env), "CLAUDE_CODE_OAUTH_TOKEN=")
	assert.Contains(t, string(env), "ANTHROPIC_API_KEY=")
}

func TestCreateRunnerFiles_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()

	// Pre-create files with stale content
	stale := []byte("stale content")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile.runner"), stale, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "entrypoint.herd.sh"), stale, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docker-compose.herd.yml"), stale, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env.herd.example"), stale, 0644))

	require.NoError(t, createRunnerFiles(dir, "org", "repo"))

	// All files should be overwritten with embedded content
	for _, name := range []string{"Dockerfile.runner", "entrypoint.herd.sh", "docker-compose.herd.yml", ".env.herd.example"} {
		content, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		assert.NotEqual(t, stale, content, "%s should be overwritten", name)
		assert.True(t, len(content) > 0, "%s should not be empty", name)
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

func TestCommitInitFiles_BranchNaming(t *testing.T) {
	oldVersion := version
	defer func() { version = oldVersion }()

	version = "v0.2.0"
	dir := setupTestGitRepoWithInitFiles(t)

	// commitInitFiles will fail at push (no remote), but we can verify branch behavior
	err := commitInitFiles(dir, "test-org", "test-repo")
	// Expect push failure since there's no real remote
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "git push")

	// Should be back on main even after push failure
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, "main", strings.TrimSpace(string(out)))
}

func TestCommitInitFiles_RerunWithExistingBranch(t *testing.T) {
	oldVersion := version
	defer func() { version = oldVersion }()

	version = "v0.3.0"
	dir := setupTestGitRepoWithInitFiles(t)

	// Create a stale branch with the same name
	cmd := exec.Command("git", "checkout", "-b", "herd/init-v0.3.0")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "checkout", "main")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	// Should not error on existing branch — it deletes and recreates
	err := commitInitFiles(dir, "test-org", "test-repo")
	// Will fail at push (no real remote), but should not fail at branch creation
	if err != nil {
		assert.Contains(t, err.Error(), "git push", "should only fail at push, not branch creation")
	}
}

func TestCommitInitFiles_NothingToCommit(t *testing.T) {
	oldVersion := version
	defer func() { version = oldVersion }()

	version = "v0.4.0"
	dir := setupTestGitRepoWithInitFiles(t)

	// Commit the init files first so there's nothing new
	gitCmd(t, dir, "add", "-A")
	gitCommit(t, dir, "pre-commit init files")

	err := commitInitFiles(dir, "test-org", "test-repo")
	assert.NoError(t, err)

	// Should be back on main
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, "main", strings.TrimSpace(string(out)))

	// Versioned branch should be cleaned up
	cmd = exec.Command("git", "branch", "--list", "herd/init-v0.4.0")
	cmd.Dir = dir
	out, err = cmd.Output()
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(out)))
}

func TestCommitInitFiles_DifferentVersionsDontCollide(t *testing.T) {
	oldVersion := version
	defer func() { version = oldVersion }()

	dir := setupTestGitRepoWithInitFiles(t)

	// Simulate first init at v0.5.0
	version = "v0.5.0"
	gitCmd(t, dir, "add", "-A")
	gitCommit(t, dir, "pre-commit init files")

	// Now update a workflow and run at v0.6.0
	version = "v0.6.0"
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".github", "workflows", "herd-worker.yml"),
		[]byte("name: updated"), 0644))

	err := commitInitFiles(dir, "test-org", "test-repo")
	// Will fail at push, but branch creation should use v0.6.0
	if err != nil {
		assert.Contains(t, err.Error(), "git push")
	}
}

// setupTestGitRepoWithInitFiles creates a test git repo with the files commitInitFiles expects.
func setupTestGitRepoWithInitFiles(t *testing.T) string {
	t.Helper()
	dir := setupTestGitRepoWithCommit(t, "git@github.com:test-org/test-repo.git")

	// Create the files that herd init produces
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".herdos.yml"), []byte("version: 1"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".herd/state/\n.env\n"), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".herd"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".herd", "planner.md"), []byte{}, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".herd", "worker.md"), []byte{}, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".herd", "integrator.md"), []byte{}, 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".github", "workflows", "herd-worker.yml"), []byte("name: worker"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".github", "workflows", "herd-monitor.yml"), []byte("name: monitor"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".github", "workflows", "herd-integrator.yml"), []byte("name: integrator"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile.runner"), []byte("FROM ubuntu"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "entrypoint.herd.sh"), []byte("#!/bin/bash"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docker-compose.herd.yml"), []byte("services:"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env.herd.example"), []byte("GITHUB_TOKEN="), 0644))

	return dir
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, string(out))
}

func gitCommit(t *testing.T, dir, msg string) {
	t.Helper()
	cmd := exec.Command("git", "commit", "-m", msg)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git commit: %s", string(out))
}

// setupTestGitRepoWithCommit creates a test git repo with an initial commit on main.
func setupTestGitRepoWithCommit(t *testing.T, remoteURL string) string {
	t.Helper()
	dir := setupTestGitRepo(t, remoteURL)

	// Create an initial commit so we have a main branch
	readme := filepath.Join(dir, "README.md")
	require.NoError(t, os.WriteFile(readme, []byte("# test"), 0644))
	cmd := exec.Command("git", "add", "README.md")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "commit", "-m", "initial commit")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	require.NoError(t, cmd.Run())

	// Rename to main if needed
	cmd = exec.Command("git", "branch", "-M", "main")
	cmd.Dir = dir
	_ = cmd.Run()

	// Set git identity for the repo (CI runners may not have global config)
	gitCmd(t, dir, "config", "user.name", "test")
	gitCmd(t, dir, "config", "user.email", "test@test.com")

	return dir
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
