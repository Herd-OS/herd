package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/config"
	cpclient "github.com/herd-os/herd/internal/controlplane/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeInitAuthorizer struct {
	token string
	err   error
}

func (a fakeInitAuthorizer) SetupToken(context.Context) (string, error) {
	return a.token, a.err
}

type fakeInitRegistrar struct {
	resp cpclient.RegisterRepositoryResponse
	err  error
	reqs []cpclient.RegisterRepositoryRequest
}

func (r *fakeInitRegistrar) RegisterRepository(_ context.Context, req cpclient.RegisterRepositoryRequest) (cpclient.RegisterRepositoryResponse, error) {
	r.reqs = append(r.reqs, req)
	return r.resp, r.err
}

func withFakeInitRegistration(t *testing.T, token string, resp cpclient.RegisterRepositoryResponse, err error) *fakeInitRegistrar {
	t.Helper()
	oldAuth := newSetupAuthorizer
	oldRegistrar := newRepositoryRegistrar
	reg := &fakeInitRegistrar{resp: resp, err: err}
	newSetupAuthorizer = func() setupAuthorizer {
		return fakeInitAuthorizer{token: token}
	}
	newRepositoryRegistrar = func(string) (repositoryRegistrar, error) {
		return reg, nil
	}
	t.Cleanup(func() {
		newSetupAuthorizer = oldAuth
		newRepositoryRegistrar = oldRegistrar
	})
	return reg
}

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

func TestRegisterRepositoryForInitFailures(t *testing.T) {
	tests := []struct {
		name       string
		authErr    error
		regErr     error
		wantErrSub string
	}{
		{"gh missing", errGHMissing, nil, "gh CLI is not installed"},
		{"gh unauthenticated", errGHUnauthenticated, nil, "gh CLI is not authenticated"},
		{"gh empty token", errGHEmptyToken, nil, "empty token"},
		{"service unavailable", nil, errors.New("503 Service Unavailable"), "control plane"},
		{"app not installed", nil, errors.New("Herd GitHub App is not installed"), "GitHub App is installed"},
		{"unauthorized repo", nil, errors.New("admin access"), "admin access"},
		{"missing bootstrap token", nil, nil, "missing runner bootstrap token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldAuth := newSetupAuthorizer
			oldRegistrar := newRepositoryRegistrar
			newSetupAuthorizer = func() setupAuthorizer {
				return fakeInitAuthorizer{token: "gho_human", err: tt.authErr}
			}
			newRepositoryRegistrar = func(string) (repositoryRegistrar, error) {
				response := cpclient.RegisterRepositoryResponse{RunnerBootstrapToken: "hrb_bootstrap"}
				if tt.name == "missing bootstrap token" {
					response.RunnerBootstrapToken = ""
				}
				return &fakeInitRegistrar{resp: response, err: tt.regErr}, nil
			}
			t.Cleanup(func() {
				newSetupAuthorizer = oldAuth
				newRepositoryRegistrar = oldRegistrar
			})

			_, err := registerRepositoryForInit(context.Background(), "octo", "herd", initOptions{ControlPlaneURL: config.DefaultControlPlaneURL, AppLogin: "herd-os"})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErrSub)
			if strings.Contains(tt.name, "service unavailable") {
				assert.NotContains(t, err.Error(), "GitHub App is installed")
			}
			assert.NotContains(t, err.Error(), "gho_human")
		})
	}
}

func TestRegisterRepositoryForInitRedactsRegistrarSetupToken(t *testing.T) {
	setupToken := "gho_exact_secret"
	reg := withFakeInitRegistration(t, setupToken, cpclient.RegisterRepositoryResponse{}, fmt.Errorf("proxy logged setup_token=%s github_pat_extra_secret", setupToken))

	_, err := registerRepositoryForInit(context.Background(), "octo", "herd", initOptions{ControlPlaneURL: config.DefaultControlPlaneURL, AppLogin: "herd-os"})

	require.Error(t, err)
	assert.Len(t, reg.reqs, 1)
	assert.Equal(t, setupToken, reg.reqs[0].SetupToken)
	assert.NotContains(t, err.Error(), setupToken)
	assert.NotContains(t, err.Error(), "github_pat_extra_secret")
	assert.Contains(t, err.Error(), "[REDACTED]")
	assert.Contains(t, err.Error(), "retry `herd init` later")
}

func TestRegisterRepositoryForInitRejectsUnsafeRunnerBootstrapToken(t *testing.T) {
	withFakeInitRegistration(t, "gho_human", cpclient.RegisterRepositoryResponse{
		RunnerBootstrapToken: "hrb_valid\nOTHER=value",
	}, nil)

	_, err := registerRepositoryForInit(context.Background(), "octo", "herd", initOptions{ControlPlaneURL: config.DefaultControlPlaneURL, AppLogin: "herd-os"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid runner bootstrap token")
	assert.Contains(t, err.Error(), "single-line")
}

func TestWriteRunnerEnvRejectsUnsafeRunnerBootstrapTokenBeforeWriting(t *testing.T) {
	dir := t.TempDir()

	err := writeRunnerEnv(dir, "hrb_valid\nOTHER=value", "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "single-line")
	_, statErr := os.Stat(filepath.Join(dir, ".env"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestValidatedEffectiveControlPlaneURLRejectsUnsafeValues(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr string
	}{
		{
			name:    "userinfo",
			value:   "https://user:pass@example.com",
			wantErr: "userinfo",
		},
		{
			name:    "double quote",
			value:   `https://example.com/path"x`,
			wantErr: "double quotes",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validatedEffectiveControlPlaneURL(tt.value)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestInstallWorkflows(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, installWorkflows(dir, config.Default()))

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
	require.NoError(t, installWorkflows(dir, config.Default()))

	// Get content of first workflow
	first := filepath.Join(dir, ".github", "workflows", WorkflowFiles()[0])
	content1, err := os.ReadFile(first)
	require.NoError(t, err)

	// Run again
	require.NoError(t, installWorkflows(dir, config.Default()))

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
	assert.Len(t, files, 5)
	assert.Contains(t, files, "herd-worker.yml")
	assert.Contains(t, files, "herd-review.yml")
	assert.Contains(t, files, "herd-publish-runner.yml")
	assert.Contains(t, files, "herd-monitor.yml")
	assert.Contains(t, files, "herd-integrator.yml")
}

func TestIntegratorWorkflowDoesNotDispatchIssueComments(t *testing.T) {
	content, err := workflowFS.ReadFile("workflows/herd-integrator.yml.tmpl")
	require.NoError(t, err)

	body := string(content)
	assert.NotContains(t, body, "issue_comment")
	assert.NotContains(t, body, "handle-comment")
	assert.NotContains(t, body, "github.event.comment.body")
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

	// Dockerfile.herd_runner_base is no longer generated.
	_, err := os.Stat(filepath.Join(dir, "Dockerfile.herd_runner_base"))
	assert.True(t, os.IsNotExist(err), "Dockerfile.herd_runner_base should not be created")

	// Dockerfile.herd_runner (user-owned, created on first run)
	hr, err := os.ReadFile(filepath.Join(dir, "Dockerfile.herd_runner"))
	require.NoError(t, err)
	assert.Contains(t, string(hr), "FROM ghcr.io/herd-os/herd-runner-base:")
	assert.Contains(t, string(hr), runnerImageTag(version))

	// entrypoint.herd.sh is no longer generated — baked into the published base image.
	_, statErr := os.Stat(filepath.Join(dir, "entrypoint.herd.sh"))
	assert.True(t, os.IsNotExist(statErr), "entrypoint.herd.sh should not be created")

	// docker-compose.herd.yml
	dc, err := os.ReadFile(filepath.Join(dir, "docker-compose.herd.yml"))
	require.NoError(t, err)
	assert.Contains(t, string(dc), "REPO_URL=https://github.com/my-org/my-project")
	assert.Contains(t, string(dc), "Dockerfile.herd_runner")
	assert.Contains(t, string(dc), "HERD_RUNNER_BOOTSTRAP_TOKEN=${HERD_RUNNER_BOOTSTRAP_TOKEN}")
	assert.NotContains(t, string(dc), "GITHUB_TOKEN=${GITHUB_TOKEN}")
	assert.Contains(t, string(dc), "CLAUDE_CODE_OAUTH_TOKEN=${CLAUDE_CODE_OAUTH_TOKEN:-}")
	assert.Contains(t, string(dc), "ANTHROPIC_API_KEY=${ANTHROPIC_API_KEY:-}")
	// RUNNER_UID/GID are forwarded so users can remap the in-container runner
	// user without rebuilding the image.
	assert.Contains(t, string(dc), "RUNNER_UID=${RUNNER_UID:-}")
	assert.Contains(t, string(dc), "RUNNER_GID=${RUNNER_GID:-}")

	// .env.herd.example
	env, err := os.ReadFile(filepath.Join(dir, ".env.herd.example"))
	require.NoError(t, err)
	assert.Contains(t, string(env), "HERD_RUNNER_BOOTSTRAP_TOKEN=")
	assert.NotContains(t, string(env), "GITHUB_TOKEN=")
	assert.Contains(t, string(env), "CLAUDE_CODE_OAUTH_TOKEN=")
	assert.Contains(t, string(env), "ANTHROPIC_API_KEY=")
	// RUNNER_UID / RUNNER_GID must be documented (commented out by default so
	// the build-time 1001:1001 stays the no-config behavior).
	assert.Contains(t, string(env), "# RUNNER_UID=")
	assert.Contains(t, string(env), "# RUNNER_GID=")
}

func TestCreateRunnerFiles_OverwritesHerdManaged(t *testing.T) {
	dir := t.TempDir()

	// Pre-create herd-managed files with stale content, plus an obsolete base file.
	stale := []byte("stale content")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile.herd_runner_base"), stale, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "entrypoint.herd.sh"), stale, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docker-compose.herd.yml"), stale, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env.herd.example"), stale, 0644))

	require.NoError(t, createRunnerFiles(dir, "org", "repo"))

	// Herd-managed files should be overwritten
	for _, name := range []string{"docker-compose.herd.yml", ".env.herd.example"} {
		content, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		assert.NotEqual(t, stale, content, "%s should be overwritten", name)
		assert.True(t, len(content) > 0, "%s should not be empty", name)
	}

	// An obsolete Dockerfile.herd_runner_base should be removed.
	_, err := os.Stat(filepath.Join(dir, "Dockerfile.herd_runner_base"))
	assert.True(t, os.IsNotExist(err), "stale Dockerfile.herd_runner_base should be removed")

	// A leftover entrypoint.herd.sh should be removed (now baked into the base image).
	_, err = os.Stat(filepath.Join(dir, "entrypoint.herd.sh"))
	assert.True(t, os.IsNotExist(err), "stale entrypoint.herd.sh should be removed")
}

// TestInit_DoesNotGenerateEntrypoint verifies a fresh init does not create
// entrypoint.herd.sh — it is baked into the published base image.
func TestInit_DoesNotGenerateEntrypoint(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, createRunnerFiles(dir, "org", "repo"))

	_, err := os.Stat(filepath.Join(dir, "entrypoint.herd.sh"))
	assert.True(t, os.IsNotExist(err), "entrypoint.herd.sh should not be created on fresh init")
}

// TestInit_RemovesLegacyEntrypoint verifies a leftover entrypoint.herd.sh from
// an older init is removed when re-running init.
func TestInit_RemovesLegacyEntrypoint(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "entrypoint.herd.sh"), []byte("#!/bin/bash\nlegacy"), 0755))

	require.NoError(t, createRunnerFiles(dir, "org", "repo"))

	_, err := os.Stat(filepath.Join(dir, "entrypoint.herd.sh"))
	assert.True(t, os.IsNotExist(err), "legacy entrypoint.herd.sh should be removed on re-init")
}

func TestCreateRunnerFiles_DoesNotOverwriteUserDockerfile(t *testing.T) {
	dir := t.TempDir()

	// Pre-create user-owned Dockerfile with a non-legacy FROM line and custom content
	custom := []byte("FROM ghcr.io/herd-os/herd-runner-base:v1.2.3\nRUN apt-get install -y golang-go")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile.herd_runner"), custom, 0644))

	require.NoError(t, createRunnerFiles(dir, "org", "repo"))

	// User Dockerfile should NOT be overwritten
	content, err := os.ReadFile(filepath.Join(dir, "Dockerfile.herd_runner"))
	require.NoError(t, err)
	assert.Equal(t, custom, content, "Dockerfile.herd_runner should not be overwritten")
}

func TestMigrateRunnerDockerfileFrom_BareLegacy(t *testing.T) {
	base := runnerBaseImage()
	in := []byte("FROM herd-runner-base\nRUN echo hi\n")
	out, changed := migrateRunnerDockerfileFrom(in, base)
	require.True(t, changed)
	assert.Equal(t, "FROM "+base+"\nRUN echo hi\n", string(out))
}

func TestMigrateRunnerDockerfileFrom_LegacyWithTag(t *testing.T) {
	base := runnerBaseImage()
	in := []byte("FROM herd-runner-base:latest\n")
	out, changed := migrateRunnerDockerfileFrom(in, base)
	require.True(t, changed)
	assert.Equal(t, "FROM "+base+"\n", string(out))
}

func TestMigrateRunnerDockerfileFrom_AlreadyGhcr(t *testing.T) {
	in := []byte("FROM ghcr.io/herd-os/herd-runner-base:v1.2.3\n")
	out, changed := migrateRunnerDockerfileFrom(in, runnerBaseImage())
	assert.False(t, changed)
	assert.Equal(t, in, out)
}

func TestMigrateRunnerDockerfileFrom_CustomBase(t *testing.T) {
	in := []byte("FROM ubuntu:24.04\n")
	out, changed := migrateRunnerDockerfileFrom(in, runnerBaseImage())
	assert.False(t, changed)
	assert.Equal(t, in, out)
}

func TestMigrateRunnerDockerfileFrom_PreservesCustomizations(t *testing.T) {
	base := runnerBaseImage()
	in := []byte("FROM herd-runner-base\nUSER root\nRUN apt-get update && apt-get install -y jq\nCOPY foo /foo\nUSER runner\n")
	out, changed := migrateRunnerDockerfileFrom(in, base)
	require.True(t, changed)
	expected := "FROM " + base + "\nUSER root\nRUN apt-get update && apt-get install -y jq\nCOPY foo /foo\nUSER runner\n"
	assert.Equal(t, expected, string(out))
}

func TestMigrateRunnerDockerfileFrom_LeadingWhitespace(t *testing.T) {
	base := runnerBaseImage()
	in := []byte("   FROM herd-runner-base\nRUN echo hi\n")
	out, changed := migrateRunnerDockerfileFrom(in, base)
	require.True(t, changed)
	assert.Equal(t, "FROM "+base+"\nRUN echo hi\n", string(out))
}

func TestMigrateRunnerDockerfileFrom_PreservesTrailingTokens(t *testing.T) {
	base := runnerBaseImage()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "multi-stage AS alias",
			in:   "FROM herd-runner-base AS builder\nRUN echo hi\n",
			want: "FROM " + base + " AS builder\nRUN echo hi\n",
		},
		{
			name: "multi-stage AS alias with tag",
			in:   "FROM herd-runner-base:latest AS builder\n",
			want: "FROM " + base + " AS builder\n",
		},
		{
			name: "lowercase as alias",
			in:   "FROM herd-runner-base as builder\n",
			want: "FROM " + base + " as builder\n",
		},
		{
			name: "trailing comment",
			in:   "FROM herd-runner-base # legacy local base\n",
			want: "FROM " + base + " # legacy local base\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, changed := migrateRunnerDockerfileFrom([]byte(tt.in), base)
			require.True(t, changed)
			assert.Equal(t, tt.want, string(out))
		})
	}
}

func TestCreateRunnerFiles_MigrationPreservesFileMode(t *testing.T) {
	dir := t.TempDir()
	dockerfilePath := filepath.Join(dir, "Dockerfile.herd_runner")
	legacy := "FROM herd-runner-base\nRUN apt-get update\n"
	require.NoError(t, os.WriteFile(dockerfilePath, []byte(legacy), 0600))
	require.NoError(t, os.Chmod(dockerfilePath, 0600))

	require.NoError(t, createRunnerFiles(dir, "acme", "widget"))

	info, err := os.Stat(dockerfilePath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm(),
		"migrated Dockerfile.herd_runner should retain its pre-migration permissions")
}

func TestCreateRunnerFiles_MigratesExistingDockerfile(t *testing.T) {
	dir := t.TempDir()
	dockerfilePath := filepath.Join(dir, "Dockerfile.herd_runner")
	legacy := "FROM herd-runner-base\nRUN apt-get update && apt-get install -y jq\n"
	require.NoError(t, os.WriteFile(dockerfilePath, []byte(legacy), 0644))

	require.NoError(t, createRunnerFiles(dir, "acme", "widget"))

	got, err := os.ReadFile(dockerfilePath)
	require.NoError(t, err)
	assert.Contains(t, string(got), "FROM ghcr.io/herd-os/herd-runner-base:")
	assert.NotContains(t, string(got), "FROM herd-runner-base\n")
	assert.Contains(t, string(got), "RUN apt-get update && apt-get install -y jq")
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

func TestProgressDirNotGitignored(t *testing.T) {
	// .herd/progress/ must NOT be gitignored — workers commit and push these files
	// so retried workers and the integrator can access them via git.
	source, err := os.ReadFile("init.go")
	require.NoError(t, err)
	src := string(source)

	assert.NotContains(t, src, `ensureGitignore(dir, ".herd/progress/")`,
		".herd/progress/ must not be added to .gitignore — progress files are shared via git")
	// .herd/state/ should still be gitignored (local-only state)
	assert.Contains(t, src, `ensureGitignore(dir, ".herd/state/")`,
		".herd/state/ should remain gitignored")
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
	assert.Contains(t, rendered, "HERD_RUNNER_BOOTSTRAP_TOKEN=${HERD_RUNNER_BOOTSTRAP_TOKEN}")
	assert.NotContains(t, rendered, "GITHUB_TOKEN=${GITHUB_TOKEN}")
	assert.Contains(t, rendered, "CLAUDE_CODE_OAUTH_TOKEN=${CLAUDE_CODE_OAUTH_TOKEN:-}")
	assert.Contains(t, rendered, "ANTHROPIC_API_KEY=${ANTHROPIC_API_KEY:-}")
}

func TestCreateRunnerFilesWithBootstrapWritesEnv(t *testing.T) {
	tests := []struct {
		name              string
		controlPlaneURL   string
		wantControlPlane  bool
		wantComposeURL    bool
		existingEnv       string
		existingGitignore string
		wantPreservedLine string
	}{
		{
			name:              "hosted omits default URL",
			controlPlaneURL:   config.DefaultControlPlaneURL,
			existingEnv:       "CLAUDE_CODE_OAUTH_TOKEN=claude\nHERD_CONTROL_PLANE_URL=https://old.example\n",
			existingGitignore: "dist",
			wantPreservedLine: "CLAUDE_CODE_OAUTH_TOKEN=claude",
		},
		{
			name:              "self-hosted persists URL",
			controlPlaneURL:   "https://herd.example.com",
			existingGitignore: "build\n",
			wantControlPlane:  true,
			wantComposeURL:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.existingEnv != "" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte(tt.existingEnv), 0600))
			}
			if tt.existingGitignore != "" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(tt.existingGitignore), 0644))
			}

			require.NoError(t, createRunnerFilesWithBootstrap(dir, "octo", "herd", "hrb_bootstrap", tt.controlPlaneURL))

			env, err := os.ReadFile(filepath.Join(dir, ".env"))
			require.NoError(t, err)
			assert.Contains(t, string(env), "HERD_RUNNER_BOOTSTRAP_TOKEN=hrb_bootstrap")
			assert.NotContains(t, string(env), "gho_human")
			if tt.wantPreservedLine != "" {
				assert.Contains(t, string(env), tt.wantPreservedLine)
			}
			if tt.wantControlPlane {
				assert.Contains(t, string(env), "HERD_CONTROL_PLANE_URL=https://herd.example.com")
			} else {
				assert.NotContains(t, string(env), "HERD_CONTROL_PLANE_URL=")
			}

			compose, err := os.ReadFile(filepath.Join(dir, "docker-compose.herd.yml"))
			require.NoError(t, err)
			if tt.wantComposeURL {
				assert.Contains(t, string(compose), `"HERD_CONTROL_PLANE_URL=https://herd.example.com"`)
			} else {
				assert.NotContains(t, string(compose), "HERD_CONTROL_PLANE_URL")
			}

			gitignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
			require.NoError(t, err)
			assert.Contains(t, string(gitignore), ".env")
			assert.NotContains(t, string(gitignore), "hrb_bootstrap")

			example, err := os.ReadFile(filepath.Join(dir, ".env.herd.example"))
			require.NoError(t, err)
			assert.NotContains(t, string(example), "hrb_bootstrap")
		})
	}
}

func TestCreateRunnerFilesWithBootstrapRejectsInvalidControlPlaneURL(t *testing.T) {
	tests := []string{
		"https://herd.example.com\nHERD_RUNNER_BOOTSTRAP_TOKEN=leak",
		"herd.example.com",
		"ftp://herd.example.com",
	}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			dir := t.TempDir()

			err := createRunnerFilesWithBootstrap(dir, "octo", "herd", "hrb_bootstrap", value)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "control-plane URL")
			_, statErr := os.Stat(filepath.Join(dir, ".env"))
			assert.True(t, os.IsNotExist(statErr))
		})
	}
}

func TestRenderDockerCompose_SingleService(t *testing.T) {
	rendered, err := renderDockerCompose("acme", "widgets")
	require.NoError(t, err)

	// Exactly one worker service block scaled to the 3-replica default.
	assert.Contains(t, rendered, "  worker:")
	assert.Contains(t, rendered, "deploy:\n      replicas: 3")
	// Exactly one top-level codex-auth volume entry.
	assert.Contains(t, rendered, "volumes:\n  codex-auth:\n")
	assert.Equal(t, 1, strings.Count(rendered, "  codex-auth:"))
	// Core wiring.
	assert.Contains(t, rendered, "RUNNER_LABELS=herd-worker")
	assert.Contains(t, rendered, "REPO_URL=https://github.com/acme/widgets")
	assert.NotContains(t, rendered, "CODEX_AUTH_JSON")
	// No multi-replica or unrendered-template artifacts.
	assert.NotContains(t, rendered, "herd-worker-1")
	assert.NotContains(t, rendered, "codex-auth-1")
	// No indexed per-replica seed variants. Asserting the bare prefix covers
	// every numbered variant without embedding a literal digit.
	assert.NotContains(t, rendered, "CODEX_AUTH_JSON_")
	assert.NotContains(t, rendered, "{{")
}

func TestRunnerDockerfileTemplate_BaseFromLine(t *testing.T) {
	oldVersion := version
	defer func() { version = oldVersion }()

	tests := []struct {
		name     string
		version  string
		wantFrom string
	}{
		{"released version pins exact tag", "v1.4.2", "FROM ghcr.io/herd-os/herd-runner-base:v1.4.2"},
		{"dev version pins latest", "dev", "FROM ghcr.io/herd-os/herd-runner-base:latest"},
		{"empty version pins latest", "", "FROM ghcr.io/herd-os/herd-runner-base:latest"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version = tt.version
			content, err := renderHerdRunnerDockerfile(runnerBaseImage())
			require.NoError(t, err)
			out := string(content)

			assert.Contains(t, out, tt.wantFrom)
			assert.Contains(t, out, "FROM ghcr.io/herd-os/herd-runner-base:"+runnerImageTag(tt.version))
			// New header comment block.
			assert.Contains(t, out, "# Base image:")
			assert.Contains(t, out, "ghcr.io/herd-os/herd-runner-base")
			assert.NotContains(t, out, "{{.BaseImage}}", "template must be fully rendered")
			// The wrapper must NOT end with `USER runner` — that opts the
			// container out of the entrypoint's RUNNER_UID remap (the
			// entrypoint detects a non-root start and skips the remap path
			// for backward compat). Use TrimSpace + HasSuffix instead of a
			// substring check so a `USER runner` somewhere in a multi-line
			// example comment wouldn't false-trigger.
			trimmed := strings.TrimSpace(out)
			assert.False(t,
				strings.HasSuffix(trimmed, "USER runner"),
				"rendered Dockerfile.herd_runner must not end with `USER runner`; "+
					"that disables RUNNER_UID remap",
			)
		})
	}
}

func TestMergeComposeOverride(t *testing.T) {
	base := []byte(`# Header comment
services:
  worker:
    build:
      dockerfile: Dockerfile.herd_runner
    environment:
      - GITHUB_TOKEN=test
`)

	override := []byte(`services:
  worker:
    build:
      args:
        BUNDLE_TOKEN: secret
    environment:
      - EXTRA_VAR=value
`)

	merged, err := mergeComposeOverride(base, override)
	require.NoError(t, err)
	result := string(merged)

	// Header preserved
	assert.Contains(t, result, "# Header comment")
	// Base values preserved
	assert.Contains(t, result, "Dockerfile.herd_runner")
	// Override values merged
	assert.Contains(t, result, "BUNDLE_TOKEN")
}

func TestMergeComposeOverride_NoOverride(t *testing.T) {
	base := []byte(`services:
  worker:
    build:
      dockerfile: Dockerfile.herd_runner
`)

	// Merging empty override should return base content
	merged, err := mergeComposeOverride(base, []byte(`{}`))
	require.NoError(t, err)
	assert.Contains(t, string(merged), "Dockerfile.herd_runner")
}

func TestMergeComposeOverride_InvalidYAML(t *testing.T) {
	base := []byte(`services:
  worker:
    build:
      dockerfile: Dockerfile.herd_runner
`)
	_, err := mergeComposeOverride(base, []byte(`invalid: yaml: [`))
	assert.Error(t, err)
}

func TestExtractYAMLHeader(t *testing.T) {
	input := "# Comment 1\n# Comment 2\n\nservices:\n  worker:\n"
	header := extractYAMLHeader(input)
	assert.Equal(t, "# Comment 1\n# Comment 2\n\n", header)
}

func TestExtractYAMLHeader_NoComments(t *testing.T) {
	input := "services:\n  worker:\n"
	header := extractYAMLHeader(input)
	assert.Equal(t, "", header)
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

func TestSelectInitMessages(t *testing.T) {
	tests := []struct {
		name      string
		previous  string
		current   string
		wantTitle string
		wantBody  string
	}{
		{
			name:      "fresh install",
			previous:  "",
			current:   "v1.2.3",
			wantTitle: "Install HerdOS v1.2.3",
			wantBody:  "Installs HerdOS workflows and runner infrastructure at v1.2.3.\n\nCreated by `herd init`.",
		},
		{
			name:      "same version sync",
			previous:  "v1.2.3",
			current:   "v1.2.3",
			wantTitle: "Sync HerdOS files",
			wantBody:  "Regenerates HerdOS workflows and runner infrastructure from current .herdos.yml.\n\nCreated by `herd init`.",
		},
		{
			name:      "version update",
			previous:  "v1.0.0",
			current:   "v1.2.3",
			wantTitle: "Update HerdOS to v1.2.3",
			wantBody:  "Updates HerdOS workflows and runner infrastructure from v1.0.0 to v1.2.3.\n\nCreated by `herd init`.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs := selectInitMessages(tt.previous, tt.current)
			assert.Equal(t, tt.wantTitle, msgs.Title)
			assert.Equal(t, tt.wantBody, msgs.Body)
		})
	}
}

func TestReadPreviousInitVersion_Missing(t *testing.T) {
	dir := t.TempDir()
	assert.Empty(t, readPreviousInitVersion(dir))
}

func TestReadPreviousInitVersion_Trims(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".herd", "state"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".herd", "state", "version"), []byte("v1.2.3\n"), 0644))
	assert.Equal(t, "v1.2.3", readPreviousInitVersion(dir))
}

func TestWriteInitVersion(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, writeInitVersion(dir, "v1.2.3"))

	content, err := os.ReadFile(filepath.Join(dir, ".herd", "state", "version"))
	require.NoError(t, err)
	assert.Equal(t, "v1.2.3\n", string(content))
	assert.Equal(t, "v1.2.3", readPreviousInitVersion(dir))
}

func TestCommitInitFiles_FreshInstall_UsesInstallTitle(t *testing.T) {
	oldVersion := version
	defer func() { version = oldVersion }()
	version = "v1.2.3"

	dir := setupTestGitRepoWithInitFilesAndRemote(t)

	// Ensure no prior state file exists.
	_, err := os.Stat(filepath.Join(dir, ".herd", "state", "version"))
	require.True(t, os.IsNotExist(err), "expected no prior version state file, got err=%v", err)

	require.NoError(t, commitInitFiles(dir, "test-org", "test-repo"))

	msg := latestRemoteCommitMessage(t, dir, "herd/init-v1.2.3")
	assert.Equal(t, "Install HerdOS v1.2.3", msg)
}

func TestCommitInitFiles_SameVersion_UsesSyncTitle(t *testing.T) {
	oldVersion := version
	defer func() { version = oldVersion }()
	version = "v1.2.3"

	dir := setupTestGitRepoWithInitFilesAndRemote(t)

	// Pre-populate the state file with the same version.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".herd", "state"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".herd", "state", "version"), []byte("v1.2.3\n"), 0644))

	require.NoError(t, commitInitFiles(dir, "test-org", "test-repo"))

	msg := latestRemoteCommitMessage(t, dir, "herd/init-v1.2.3")
	assert.Equal(t, "Sync HerdOS files", msg)
}

func TestCommitInitFiles_DifferentVersion_UsesUpdateTitle(t *testing.T) {
	oldVersion := version
	defer func() { version = oldVersion }()
	version = "v1.2.3"

	dir := setupTestGitRepoWithInitFilesAndRemote(t)

	// Pre-populate the state file with an older version.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".herd", "state"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".herd", "state", "version"), []byte("v1.0.0\n"), 0644))

	require.NoError(t, commitInitFiles(dir, "test-org", "test-repo"))

	msg := latestRemoteCommitMessage(t, dir, "herd/init-v1.2.3")
	assert.Equal(t, "Update HerdOS to v1.2.3", msg)
}

func TestCommitInitFiles_WritesVersionFile(t *testing.T) {
	oldVersion := version
	defer func() { version = oldVersion }()
	version = "v1.2.3"

	dir := setupTestGitRepoWithInitFilesAndRemote(t)

	require.NoError(t, commitInitFiles(dir, "test-org", "test-repo"))

	content, err := os.ReadFile(filepath.Join(dir, ".herd", "state", "version"))
	require.NoError(t, err)
	assert.Equal(t, "v1.2.3", strings.TrimSpace(string(content)))
}

func TestCommitInitFiles_WritesVersionFile_NothingToCommit(t *testing.T) {
	oldVersion := version
	defer func() { version = oldVersion }()
	version = "v1.2.3"

	dir := setupTestGitRepoWithInitFilesAndRemote(t)

	// Commit init files first so commitInitFiles short-circuits.
	gitCmd(t, dir, "add", "-A")
	gitCommit(t, dir, "pre-commit init files")

	require.NoError(t, commitInitFiles(dir, "test-org", "test-repo"))

	content, err := os.ReadFile(filepath.Join(dir, ".herd", "state", "version"))
	require.NoError(t, err)
	assert.Equal(t, "v1.2.3", strings.TrimSpace(string(content)))
}

// setupTestGitRepoWithInitFilesAndRemote creates a test git repo whose origin
// points to a local bare repo, so that `git push` succeeds inside tests.
func setupTestGitRepoWithInitFilesAndRemote(t *testing.T) string {
	t.Helper()

	// Create the bare remote in a separate temp dir.
	bareDir := t.TempDir()
	cmd := exec.Command("git", "init", "--bare")
	cmd.Dir = bareDir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git init --bare: %s", string(out))

	// Create the working repo with the bare repo as origin.
	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "remote", "add", "origin", bareDir)
	gitCmd(t, dir, "config", "user.name", "test")
	gitCmd(t, dir, "config", "user.email", "test@test.com")

	// Initial commit on main.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test"), 0644))
	gitCmd(t, dir, "add", "README.md")
	gitCommit(t, dir, "initial commit")
	gitCmd(t, dir, "branch", "-M", "main")

	// Create files herd init produces.
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
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile.herd_runner"), []byte("FROM ghcr.io/herd-os/herd-runner-base:latest"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docker-compose.herd.yml"), []byte("services:"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env.herd.example"), []byte("GITHUB_TOKEN="), 0644))

	return dir
}

// latestRemoteCommitMessage returns the subject of the latest commit on the
// given branch in the local repo's `origin` remote-tracking ref.
// `commitInitFiles` deletes the local branch on return but the remote-tracking
// ref and the bare remote still hold the commit, so we read from there.
func latestRemoteCommitMessage(t *testing.T, dir, branch string) string {
	t.Helper()
	cmd := exec.Command("git", "log", "-1", "--format=%s", "refs/remotes/origin/"+branch)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git log: %s", string(out))
	return strings.TrimSpace(string(out))
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
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile.herd_runner"), []byte("FROM ghcr.io/herd-os/herd-runner-base:latest"), 0644))
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

// TestRunInitSkipLabelsEndToEnd is the integration-level regression guard
// for the release self-update path: it exercises the full runInit flow with
// skipLabels=true and verifies every herd-managed file is produced.
//
// Test setup pre-creates the herd/init-<version> branch so that commitInitFiles
// fails fast at "git checkout -b" before its deferred branch-cleanup fires.
// Without this, the deferred switch-back-to-main wipes the just-committed
// herd files from the working tree, leaving nothing to assert on.
//
// skipLabels=true guarantees no GitHub API call is attempted: runInit
// short-circuits on the !skipLabels branch before reaching createLabels.
func TestRunInitSkipLabelsEndToEnd(t *testing.T) {
	oldVersion := version
	defer func() { version = oldVersion }()
	version = "v9.9.9-test"

	dir := setupTestGitRepoWithCommit(t, "git@github.com:test-org/test-repo.git")

	gitCmd(t, dir, "checkout", "-b", "herd/init-"+version)

	oldWd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldWd) }()
	require.NoError(t, os.Chdir(dir))

	reg := withFakeInitRegistration(t, "gho_human", cpclient.RegisterRepositoryResponse{
		RepositoryID:         10,
		InstallationID:       20,
		RunnerBootstrapToken: "hrb_bootstrap",
	}, nil)
	require.NoError(t, runInit(true, false))
	require.Len(t, reg.reqs, 1)
	assert.Equal(t, "gho_human", reg.reqs[0].SetupToken)

	herdosYml := filepath.Join(dir, ".herdos.yml")
	info, err := os.Stat(herdosYml)
	require.NoError(t, err, ".herdos.yml should exist")
	assert.Greater(t, info.Size(), int64(0), ".herdos.yml should not be empty")

	gi, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(gi), ".herd/state/")
	assert.Contains(t, string(gi), ".env")

	for _, name := range RoleInstructionFiles() {
		path := filepath.Join(dir, ".herd", name)
		_, err := os.Stat(path)
		assert.NoError(t, err, ".herd/%s should exist", name)
	}

	for _, name := range WorkflowFiles() {
		t.Run("workflow_"+name, func(t *testing.T) {
			path := filepath.Join(dir, ".github", "workflows", name)
			info, err := os.Stat(path)
			require.NoError(t, err)
			assert.Greater(t, info.Size(), int64(0), "%s should not be empty", name)
		})
	}

	_, err = os.Stat(filepath.Join(dir, "Dockerfile.herd_runner_base"))
	assert.True(t, os.IsNotExist(err), "Dockerfile.herd_runner_base should not be created")

	dfRunner, err := os.ReadFile(filepath.Join(dir, "Dockerfile.herd_runner"))
	require.NoError(t, err)
	assert.Contains(t, string(dfRunner), "FROM ghcr.io/herd-os/herd-runner-base:")

	_, epErr := os.Stat(filepath.Join(dir, "entrypoint.herd.sh"))
	assert.True(t, os.IsNotExist(epErr), "entrypoint.herd.sh should not be created")

	dc, err := os.ReadFile(filepath.Join(dir, "docker-compose.herd.yml"))
	require.NoError(t, err)
	assert.Contains(t, string(dc), "REPO_URL=https://github.com/test-org/test-repo")
	assert.NotContains(t, string(dc), "herd-runner-base", "compose should no longer define a herd-runner-base service")
	assert.NotContains(t, string(dc), "gho_human")

	env, err := os.ReadFile(filepath.Join(dir, ".env"))
	require.NoError(t, err)
	assert.Contains(t, string(env), "HERD_RUNNER_BOOTSTRAP_TOKEN=hrb_bootstrap")
	assert.NotContains(t, string(env), "gho_human")

	envEx, err := os.ReadFile(filepath.Join(dir, ".env.herd.example"))
	require.NoError(t, err)
	assert.Contains(t, string(envEx), "HERD_RUNNER_BOOTSTRAP_TOKEN=")
	assert.Contains(t, string(envEx), "do not overwrite a generated token")
	assert.NotContains(t, string(envEx), "cp .env.herd.example .env")
	assert.NotContains(t, string(envEx), "GITHUB_TOKEN=")
}

// TestRunInitSkipLabelsIdempotent verifies that running runInit twice in the
// same dir does not produce duplicate .gitignore entries, does not overwrite
// .herdos.yml, and produces byte-identical herd-managed workflow/runner files.
// See TestRunInitSkipLabelsEndToEnd for why we pre-create the herd/init-<version>
// branch.
func TestRunInitSkipLabelsIdempotent(t *testing.T) {
	oldVersion := version
	defer func() { version = oldVersion }()
	version = "v9.9.9-test"

	dir := setupTestGitRepoWithCommit(t, "git@github.com:test-org/test-repo.git")

	gitCmd(t, dir, "checkout", "-b", "herd/init-"+version)

	oldWd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldWd) }()
	require.NoError(t, os.Chdir(dir))

	withFakeInitRegistration(t, "gho_human", cpclient.RegisterRepositoryResponse{
		RepositoryID:         10,
		InstallationID:       20,
		RunnerBootstrapToken: "hrb_bootstrap",
	}, nil)
	require.NoError(t, runInit(true, false), "first runInit")

	herdosFirst, err := os.ReadFile(filepath.Join(dir, ".herdos.yml"))
	require.NoError(t, err)

	gitignoreFirst, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)

	workflowFirst := make(map[string][]byte, len(WorkflowFiles()))
	for _, name := range WorkflowFiles() {
		data, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", name))
		require.NoError(t, err)
		workflowFirst[name] = data
	}

	runnerFiles := []string{
		"Dockerfile.herd_runner",
		"docker-compose.herd.yml",
		".env.herd.example",
	}
	runnerFirst := make(map[string][]byte, len(runnerFiles))
	for _, name := range runnerFiles {
		data, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		runnerFirst[name] = data
	}

	require.NoError(t, runInit(true, false), "second runInit")

	herdosSecond, err := os.ReadFile(filepath.Join(dir, ".herdos.yml"))
	require.NoError(t, err)
	assert.True(t, bytes.Equal(herdosFirst, herdosSecond), ".herdos.yml should not change on re-run")

	gitignoreSecond, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, string(gitignoreFirst), string(gitignoreSecond), ".gitignore should not change on re-run")
	gitignoreLines := strings.Split(strings.TrimSpace(string(gitignoreSecond)), "\n")
	assert.Equal(t, 1, countMatching(gitignoreLines, ".herd/state/"), ".gitignore should not duplicate .herd/state/")
	assert.Equal(t, 1, countMatching(gitignoreLines, ".env"), ".gitignore should not duplicate .env")

	for _, name := range WorkflowFiles() {
		data, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", name))
		require.NoError(t, err)
		assert.True(t, bytes.Equal(workflowFirst[name], data), "%s should be byte-identical on re-run", name)
	}

	for _, name := range runnerFiles {
		data, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		assert.True(t, bytes.Equal(runnerFirst[name], data), "%s should be byte-identical on re-run", name)
	}
}

func countMatching(lines []string, want string) int {
	n := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == want {
			n++
		}
	}
	return n
}

// setupCleanInitRepo creates a temp git repo with origin pointing at
// git@github.com:acme/widgets.git and writes every herd-managed file
// (.herdos.yml, the workflow files, runner infrastructure, and Dockerfile.herd_runner)
// such that CheckHerdFilesUpToDate(dir) reports no drift.
func setupCleanInitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "remote", "add", "origin", "git@github.com:acme/widgets.git")
	gitCmd(t, dir, "config", "user.name", "test")
	gitCmd(t, dir, "config", "user.email", "test@test.com")

	cfg := config.Default()
	cfg.Platform.Owner = "acme"
	cfg.Platform.Repo = "widgets"
	require.NoError(t, config.Save(dir, cfg))

	require.NoError(t, installManagedFilesOnly(dir, "acme", "widgets", cfg))

	herdRunner, err := renderHerdRunnerDockerfile(runnerBaseImage())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile.herd_runner"), herdRunner, 0644))

	return dir
}

func TestRunInitCheck_AllUpToDate(t *testing.T) {
	dir := setupCleanInitRepo(t)

	drifted, err := CheckHerdFilesUpToDate(dir)
	require.NoError(t, err)
	assert.Empty(t, drifted, "freshly-installed repo should have no drift, got: %+v", drifted)
}

func TestRunInitCheck_DetectsWorkflowDrift(t *testing.T) {
	dir := setupCleanInitRepo(t)

	target := filepath.Join(dir, ".github", "workflows", "herd-worker.yml")
	require.NoError(t, os.WriteFile(target, []byte("# tampered\n"), 0644))

	drifted, err := CheckHerdFilesUpToDate(dir)
	require.NoError(t, err)
	require.Len(t, drifted, 1)
	assert.Equal(t, ".github/workflows/herd-worker.yml", drifted[0].Path)
	assert.Equal(t, "content differs", drifted[0].Reason)
}

func TestRunInitCheck_HonorsOverride(t *testing.T) {
	dir := setupCleanInitRepo(t)

	overrideYAML := []byte(`services:
  worker:
    environment:
      - EXTRA_VAR=hello
`)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docker-compose.herd.override.yml"), overrideYAML, 0644))

	cfg, err := config.Load(dir)
	require.NoError(t, err)
	require.NoError(t, installManagedFilesOnly(dir, "acme", "widgets", cfg))

	t.Run("merged_compose_matches", func(t *testing.T) {
		drifted, err := CheckHerdFilesUpToDate(dir)
		require.NoError(t, err)
		for _, d := range drifted {
			assert.NotEqual(t, "docker-compose.herd.yml", d.Path,
				"docker-compose.herd.yml should match the freshly merged render, got drift: %+v", d)
		}
	})

	t.Run("stale_compose_drifts", func(t *testing.T) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "docker-compose.herd.yml"), []byte("# stale\n"), 0644))

		drifted, err := CheckHerdFilesUpToDate(dir)
		require.NoError(t, err)
		var found bool
		for _, d := range drifted {
			if d.Path == "docker-compose.herd.yml" {
				found = true
				assert.Equal(t, "content differs", d.Reason)
			}
		}
		assert.True(t, found, "expected docker-compose.herd.yml in drift, got: %+v", drifted)
	})
}

func TestRunInitCheck_MissingDockerfileHerdRunner(t *testing.T) {
	dir := setupCleanInitRepo(t)

	require.NoError(t, os.Remove(filepath.Join(dir, "Dockerfile.herd_runner")))

	drifted, err := CheckHerdFilesUpToDate(dir)
	require.NoError(t, err)

	var found bool
	for _, d := range drifted {
		if d.Path == "Dockerfile.herd_runner" {
			found = true
			assert.Equal(t, "would be created", d.Reason)
		}
	}
	assert.True(t, found, "expected Dockerfile.herd_runner in drift, got: %+v", drifted)
}

func TestRunInitCheck_WritesNothing(t *testing.T) {
	dir := setupCleanInitRepo(t)

	type fileSnap struct {
		mtime int64
		size  int64
		data  []byte
	}

	collect := func() map[string]fileSnap {
		snaps := make(map[string]fileSnap)
		require.NoError(t, filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(dir, path)
			require.NoError(t, err)
			if strings.HasPrefix(rel, ".git"+string(filepath.Separator)) || rel == ".git" {
				return nil
			}
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			snaps[rel] = fileSnap{
				mtime: info.ModTime().UnixNano(),
				size:  info.Size(),
				data:  data,
			}
			return nil
		}))
		return snaps
	}

	before := collect()

	oldWd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldWd) }()
	require.NoError(t, os.Chdir(dir))

	require.NoError(t, runInitCheck())

	after := collect()
	assert.Equal(t, len(before), len(after), "no files should be created or removed by --check")
	for path, b := range before {
		a, ok := after[path]
		require.True(t, ok, "%s disappeared after --check", path)
		assert.Equal(t, b.size, a.size, "%s size changed", path)
		assert.True(t, bytes.Equal(b.data, a.data), "%s content changed", path)
	}
}

func TestRunInitCheck_ReturnsDriftSentinelOnDrift(t *testing.T) {
	dir := setupCleanInitRepo(t)

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".github", "workflows", "herd-worker.yml"),
		[]byte("# tampered\n"), 0644))

	oldWd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldWd) }()
	require.NoError(t, os.Chdir(dir))

	err = runInitCheck()
	require.Error(t, err)
	assert.True(t, errors.Is(err, errCheckDrift), "expected errCheckDrift, got: %v", err)
}

func TestFirstDiffLines_TruncatesAt5(t *testing.T) {
	tests := []struct {
		name string
		old  string
		new  string
		max  int
	}{
		{
			name: "many lines added",
			old:  "a\nb\nc\n",
			new:  "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n",
			max:  5,
		},
		{
			name: "many lines removed",
			old:  "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n",
			new:  "a\nb\nc\n",
			max:  5,
		},
		{
			name: "completely different",
			old:  "alpha\nbravo\ncharlie\ndelta\necho\nfoxtrot\ngolf\n",
			new:  "one\ntwo\nthree\nfour\nfive\nsix\nseven\n",
			max:  5,
		},
		{
			name: "identical content",
			old:  "a\nb\nc\n",
			new:  "a\nb\nc\n",
			max:  5,
		},
		{
			name: "single line change",
			old:  "a\nb\nc\n",
			new:  "a\nB\nc\n",
			max:  5,
		},
		{
			name: "empty old",
			old:  "",
			new:  "x\ny\nz\n1\n2\n3\n4\n",
			max:  5,
		},
		{
			name: "empty new",
			old:  "x\ny\nz\n1\n2\n3\n4\n",
			new:  "",
			max:  5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := firstDiffLines([]byte(tt.old), []byte(tt.new), tt.max)
			lines := []string{}
			if result != "" {
				lines = strings.Split(result, "\n")
			}
			assert.LessOrEqual(t, len(lines), tt.max+5,
				"output line count should not exceed max+context, got %d lines:\n%s", len(lines), result)

			nonContext := 0
			for _, l := range lines {
				if strings.HasPrefix(l, "-") || strings.HasPrefix(l, "+") {
					nonContext++
				}
			}
			assert.LessOrEqual(t, nonContext, tt.max,
				"non-context line count should not exceed max")
		})
	}
}

func TestFirstDiffLines_MaxZero(t *testing.T) {
	out := firstDiffLines([]byte("a\nb\n"), []byte("c\nd\n"), 0)
	assert.Empty(t, out)
}

func TestCheckHerdFilesUpToDate_MissingConfig(t *testing.T) {
	dir := setupCleanInitRepo(t)

	require.NoError(t, os.Remove(filepath.Join(dir, config.ConfigFile)))

	drifted, err := CheckHerdFilesUpToDate(dir)
	require.NoError(t, err)

	var found bool
	for _, d := range drifted {
		if d.Path == config.ConfigFile {
			found = true
			assert.Equal(t, "would be created", d.Reason)
		}
	}
	assert.True(t, found, "expected %s in drift, got: %+v", config.ConfigFile, drifted)
}

// TestRunInitCheck_DisplayUsesWouldChangeForMissingFiles verifies the per-file
// output uses the literal "(would change)" suffix for every drifted entry,
// including absent .herdos.yml and Dockerfile.herd_runner. The internal Reason
// field on DriftedFile may still be "would be created" — that's a separate
// data label not surfaced verbatim to users.
func TestRunInitCheck_DisplayUsesWouldChangeForMissingFiles(t *testing.T) {
	dir := setupCleanInitRepo(t)

	require.NoError(t, os.Remove(filepath.Join(dir, config.ConfigFile)))
	require.NoError(t, os.Remove(filepath.Join(dir, "Dockerfile.herd_runner")))
	require.NoError(t, os.Remove(filepath.Join(dir, "docker-compose.herd.yml")))

	oldWd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldWd) }()
	require.NoError(t, os.Chdir(dir))

	stdout, _ := captureStdio(t, func() {
		err := runInitCheck()
		require.Error(t, err)
		assert.True(t, errors.Is(err, errCheckDrift))
	})

	assert.Contains(t, stdout, config.ConfigFile+" (would change)",
		".herdos.yml line should use the literal '(would change)' suffix")
	assert.Contains(t, stdout, "Dockerfile.herd_runner (would change)",
		"Dockerfile.herd_runner line should use the literal '(would change)' suffix")
	assert.Contains(t, stdout, "docker-compose.herd.yml (would change)",
		"a missing managed file should be displayed with '(would change)'")
	assert.NotContains(t, stdout, "(would be created)",
		"per-file output should never use '(would be created)'")
}

// TestRunInitCheck_WarnsOnUnparseableOverride verifies that --check surfaces a
// stderr warning when docker-compose.herd.override.yml fails to merge, rather
// than silently rendering base-only and reporting spurious drift.
func TestRunInitCheck_WarnsOnUnparseableOverride(t *testing.T) {
	dir := setupCleanInitRepo(t)

	// Write a malformed override that will fail to YAML-unmarshal.
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "docker-compose.herd.override.yml"),
		[]byte("services:\n  worker:\n    bad: [unterminated\n"), 0644))

	oldWd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldWd) }()
	require.NoError(t, os.Chdir(dir))

	_, stderr := captureStdio(t, func() {
		_ = runInitCheck()
	})

	assert.Contains(t, stderr, "docker-compose.herd.override.yml",
		"check path should warn about the failed override merge")
}

// TestComputeManagedDrift_ReturnsRenderedFilesAndDrift verifies the helper that
// runInitCheck and CheckHerdFilesUpToDate share: a single pass returning both
// the rendered managed-file set and the drift list, so the check path doesn't
// re-render or re-load config.
func TestComputeManagedDrift_ReturnsRenderedFilesAndDrift(t *testing.T) {
	dir := setupCleanInitRepo(t)

	target := filepath.Join(dir, ".github", "workflows", "herd-worker.yml")
	require.NoError(t, os.WriteFile(target, []byte("# tampered\n"), 0644))

	cfg, cfgMissing, files, drifted, err := computeManagedDrift(dir)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.False(t, cfgMissing)
	assert.NotEmpty(t, files, "should return the rendered managed file set")

	var foundFile bool
	for _, mf := range files {
		if mf.Path == ".github/workflows/herd-worker.yml" {
			foundFile = true
			assert.NotEmpty(t, mf.Content)
		}
	}
	assert.True(t, foundFile, "rendered files should include herd-worker.yml")

	var foundDrift bool
	for _, d := range drifted {
		if d.Path == ".github/workflows/herd-worker.yml" {
			foundDrift = true
			assert.Equal(t, "content differs", d.Reason)
		}
	}
	assert.True(t, foundDrift, "drift should include the tampered workflow")
}

func TestPrintNextStepsDoesNotOverwriteGeneratedEnv(t *testing.T) {
	stdout, _ := captureStdio(t, func() {
		printNextSteps("octo", "repo")
	})

	assert.NotContains(t, stdout, "cp .env.herd.example .env")
	assert.Contains(t, stdout, "Review the .env created by herd init")
	assert.Contains(t, stdout, "Confirm HERD_RUNNER_BOOTSTRAP_TOKEN is present in .env")
	assert.Contains(t, stdout, "do not overwrite it")
}
