package artifacts

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/appauth"
	herdgit "github.com/herd-os/herd/internal/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyRejectsTargetBranchAdvanced(t *testing.T) {
	remote, source, base, head := prepareApplyRepos(t)
	advanceRemote(t, remote)
	artifact := diffArtifact(t, source, base, head)

	_, err := Apply(context.Background(), ApplyRequest{
		Repository:      "acme/widgets",
		CloneURL:        remote,
		TargetBranch:    "main",
		BaseSHA:         base,
		ExpectedHeadSHA: base,
		Artifact:        artifact,
		Identity:        DefaultIdentity("HerdOS", "herd@example.com"),
		TempDir:         t.TempDir(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "target branch advanced")
}

func TestApplyCommitsWithAppIdentityAndTrailers(t *testing.T) {
	remote, source, base, head := prepareApplyRepos(t)
	artifact := diffArtifact(t, source, base, head)

	result, err := Apply(context.Background(), ApplyRequest{
		Repository:      "acme/widgets",
		CloneURL:        remote,
		TargetBranch:    "main",
		BaseSHA:         base,
		ExpectedHeadSHA: base,
		Artifact:        artifact,
		Identity:        DefaultIdentity("herd-os[bot]", "herd@example.com"),
		Human:           HumanAttribution{Name: "Mona", Email: "mona@example.com"},
		TempDir:         t.TempDir(),
	})
	require.NoError(t, err)
	assert.Len(t, result.CommitSHA, 40)

	clone := t.TempDir()
	require.NoError(t, herdgit.Clone(remote, clone))
	g := herdgit.New(clone)
	require.NoError(t, g.Checkout("main"))
	message := gitOutput(t, clone, "log", "-1", "--pretty=%B")
	author := gitOutput(t, clone, "log", "-1", "--pretty=%an <%ae>")
	assert.Contains(t, message, "Herd-Job-ID: job-1")
	assert.Contains(t, message, "Co-authored-by: Mona <mona@example.com>")
	assert.Equal(t, "herd-os[bot] <herd@example.com>", strings.TrimSpace(author))
	assert.Equal(t, "changed\n", readFile(t, filepath.Join(clone, "file.txt")))
	assert.Equal(t, []byte{0x00, 0x01, 0xfe, 0xff}, []byte(readFile(t, filepath.Join(clone, "binary.bin"))))
}

func TestApplyAuthenticatedCloneErrorRedactsInstallationToken(t *testing.T) {
	token := "ghs_secret_installation_token"
	_, err := Apply(context.Background(), ApplyRequest{
		Repository:      "acme/widgets",
		CloneURL:        "https://example.invalid/acme/widgets.git",
		InstallationID:  123,
		TargetBranch:    "main",
		BaseSHA:         "base",
		ExpectedHeadSHA: "head",
		Artifact: ValidatedArtifact{
			Metadata: PatchMetadata{
				Repository:      "acme/widgets",
				JobID:           "job-1",
				BaseSHA:         "base",
				ExpectedHeadSHA: "head",
				Format:          FormatGitDiffBinary,
			},
			Data: []byte("diff --git a/file.txt b/file.txt\n"),
		},
		Identity:    DefaultIdentity("HerdOS", "herd@example.com"),
		TokenSource: fixedTokenSource{token: token},
		TempDir:     t.TempDir(),
	})

	require.Error(t, err)
	assert.NotContains(t, err.Error(), token)
	assert.NotContains(t, err.Error(), "x-access-token")
	assert.NotContains(t, err.Error(), base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)))
}

func TestApplyAuthenticatedCloneDoesNotPersistTokenInTempDir(t *testing.T) {
	remote, source, base, head := prepareApplyRepos(t)
	artifact := diffArtifact(t, source, base, head)
	tempDir := t.TempDir()
	token := "ghs_secret_installation_token"

	_, err := Apply(context.Background(), ApplyRequest{
		Repository:      "acme/widgets",
		CloneURL:        remote,
		InstallationID:  123,
		TargetBranch:    "main",
		BaseSHA:         base,
		ExpectedHeadSHA: base,
		Artifact:        artifact,
		Identity:        DefaultIdentity("HerdOS", "herd@example.com"),
		TokenSource:     fixedTokenSource{token: token},
		TempDir:         tempDir,
	})

	require.NoError(t, err)
	assertTempDirDoesNotContain(t, tempDir, token)
	assertTempDirDoesNotContain(t, tempDir, "x-access-token")
	assert.NotEqual(t, head, "")
}

func prepareApplyRepos(t *testing.T) (string, string, string, string) {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote.git")
	cmd := exec.Command("git", "init", "--bare", remote)
	require.NoError(t, cmd.Run())

	source := initArtifactRepo(t)
	g := herdgit.New(source)
	gitCmd(t, source, "remote", "add", "origin", remote)
	require.NoError(t, g.Push("origin", "main"))
	base := mustHead(t, g)

	require.NoError(t, os.WriteFile(filepath.Join(source, "file.txt"), []byte("changed\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(source, "binary.bin"), []byte{0x00, 0x01, 0xfe, 0xff}, 0644))
	require.NoError(t, g.Add("."))
	require.NoError(t, g.Commit("worker changes"))
	head := mustHead(t, g)
	return remote, source, base, head
}

func diffArtifact(t *testing.T, source, base, head string) ValidatedArtifact {
	t.Helper()
	diff, err := CreateBinaryDiff(source, base, head)
	require.NoError(t, err)
	return ValidatedArtifact{
		Metadata: BuildMetadata("acme/widgets", "job-1", base, base, "patch.diff", diff),
		Data:     diff,
	}
}

func advanceRemote(t *testing.T, remote string) {
	t.Helper()
	clone := t.TempDir()
	require.NoError(t, herdgit.Clone(remote, clone))
	g := herdgit.New(clone)
	require.NoError(t, g.Checkout("main"))
	require.NoError(t, g.ConfigureIdentity("Test", "test@example.com"))
	require.NoError(t, os.WriteFile(filepath.Join(clone, "advanced.txt"), []byte("advanced\n"), 0644))
	require.NoError(t, g.Add("."))
	require.NoError(t, g.Commit("advance"))
	require.NoError(t, g.Push("origin", "main"))
}

func initArtifactRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-b", "main")
	gitCmd(t, dir, "config", "user.email", "test@example.com")
	gitCmd(t, dir, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("original\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "modified.txt"), []byte("original\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "deleted.txt"), []byte("delete\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "renamed.txt"), []byte("rename\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mode.sh"), []byte("#!/bin/sh\n"), 0644))
	gitCmd(t, dir, "add", ".")
	gitCmd(t, dir, "commit", "-m", "initial")
	return dir
}

func mustHead(t *testing.T, g *herdgit.Git) string {
	t.Helper()
	sha, err := g.HeadSHA()
	require.NoError(t, err)
	return sha
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	require.NoError(t, err)
	return string(out)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

type fixedTokenSource struct {
	token string
}

func (s fixedTokenSource) InstallationToken(context.Context, int64) (appauth.InstallationToken, error) {
	return appauth.InstallationToken{Token: s.token}, nil
}

func assertTempDirDoesNotContain(t *testing.T, root string, needle string) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		require.NoError(t, err)
		if d.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		assert.NotContains(t, string(data), needle, path)
		return nil
	}))
}
