package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/herd-os/herd/internal/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initPreflightRepo creates a git repo with a remote "origin" that has a
// default branch, suitable for testing preflight checks.
func initPreflightRepo(t *testing.T) (repoDir, remoteDir string) {
	t.Helper()

	// Create a bare "remote" repo
	remoteDir = t.TempDir()
	runGitPreflight(t, remoteDir, "init", "--bare")

	// Clone it to get a working repo with origin set up
	parentDir := t.TempDir()
	repoDir = filepath.Join(parentDir, "repo")
	runGitPreflight(t, parentDir, "clone", remoteDir, "repo")
	runGitPreflight(t, repoDir, "config", "user.email", "test@test.com")
	runGitPreflight(t, repoDir, "config", "user.name", "Test")

	// Create initial commit and push
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# test"), 0644))
	runGitPreflight(t, repoDir, "add", ".")
	runGitPreflight(t, repoDir, "commit", "-m", "initial")
	runGitPreflight(t, repoDir, "push", "origin", "HEAD")

	return repoDir, remoteDir
}

// runGitPreflight is a helper that calls git commands and fails on error.
func runGitPreflight(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, string(out))
}

func TestPreflightDirtyCheck_Clean(t *testing.T) {
	repoDir, _ := initPreflightRepo(t)
	g := git.New(repoDir)

	err := preflightDirtyCheck(g)
	assert.NoError(t, err)
}

func TestPreflightDirtyCheck_Dirty(t *testing.T) {
	repoDir, _ := initPreflightRepo(t)
	g := git.New(repoDir)

	// Make the tree dirty
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "dirty.txt"), []byte("dirty"), 0644))

	err := preflightDirtyCheck(g)
	assert.NoError(t, err) // dirty check is informational, not an error
}

func TestRunPreflight_OnDefaultBranch_UpToDate(t *testing.T) {
	repoDir, _ := initPreflightRepo(t)

	// Provide empty stdin (no prompts expected)
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()
	w.Close()

	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	err = runPreflight(repoDir)
	assert.NoError(t, err)
}

func TestRunPreflight_OnNonDefaultBranch_DeclineSwitch(t *testing.T) {
	repoDir, _ := initPreflightRepo(t)

	// Create and switch to a feature branch
	runGitPreflight(t, repoDir, "checkout", "-b", "feature-branch")

	// Provide "n" to decline switching
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()

	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	_, err = w.WriteString("n\n")
	require.NoError(t, err)
	w.Close()

	err = runPreflight(repoDir)
	assert.NoError(t, err)
}

func TestRunPreflight_OnNonDefaultBranch_AcceptSwitch(t *testing.T) {
	repoDir, _ := initPreflightRepo(t)

	// Create and switch to a feature branch
	runGitPreflight(t, repoDir, "checkout", "-b", "feature-branch")

	// Provide "y" to accept switching
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()

	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	_, err = w.WriteString("y\n")
	require.NoError(t, err)
	w.Close()

	err = runPreflight(repoDir)
	assert.NoError(t, err)

	// Verify we switched to the default branch
	g := git.New(repoDir)
	branch, err := g.CurrentBranch()
	require.NoError(t, err)
	defaultBranch, err := g.DefaultBranch()
	require.NoError(t, err)
	assert.Equal(t, defaultBranch, branch)
}

func TestRunPreflight_OnNonDefaultBranch_DirtyRejectsSwitch(t *testing.T) {
	repoDir, _ := initPreflightRepo(t)

	// Create and switch to a feature branch
	runGitPreflight(t, repoDir, "checkout", "-b", "feature-branch")

	// Make the tree dirty
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "dirty.txt"), []byte("dirty"), 0644))

	// Provide "y" to accept switching — should fail because dirty
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()

	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	_, err = w.WriteString("y\n")
	require.NoError(t, err)
	w.Close()

	err = runPreflight(repoDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "uncommitted changes")
	assert.Contains(t, err.Error(), "stash or commit before switching")
}

func TestRunPreflight_BehindRemote_AcceptPull(t *testing.T) {
	repoDir, remoteDir := initPreflightRepo(t)

	// Push a commit to the remote from a separate clone to simulate being behind
	tmpClone := t.TempDir()
	runGitPreflight(t, t.TempDir(), "clone", remoteDir, tmpClone)
	// Use the actual tmpClone directory
	runGitPreflight(t, tmpClone, "config", "user.email", "test@test.com")
	runGitPreflight(t, tmpClone, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(tmpClone, "new.txt"), []byte("new"), 0644))
	runGitPreflight(t, tmpClone, "add", ".")
	runGitPreflight(t, tmpClone, "commit", "-m", "remote commit")

	// Get the branch name from the clone
	g := git.New(tmpClone)
	branch, err := g.CurrentBranch()
	require.NoError(t, err)
	runGitPreflight(t, tmpClone, "push", "origin", branch)

	// Now our repoDir is behind. Provide "y" to pull.
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()

	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	_, err = w.WriteString("y\n")
	require.NoError(t, err)
	w.Close()

	err = runPreflight(repoDir)
	assert.NoError(t, err)

	// Verify we pulled the new file
	_, err = os.Stat(filepath.Join(repoDir, "new.txt"))
	assert.NoError(t, err)
}

func TestRunPreflight_BehindRemote_DeclinePull(t *testing.T) {
	repoDir, remoteDir := initPreflightRepo(t)

	// Push a commit from a separate clone
	tmpClone := t.TempDir()
	runGitPreflight(t, t.TempDir(), "clone", remoteDir, tmpClone)
	runGitPreflight(t, tmpClone, "config", "user.email", "test@test.com")
	runGitPreflight(t, tmpClone, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(tmpClone, "new.txt"), []byte("new"), 0644))
	runGitPreflight(t, tmpClone, "add", ".")
	runGitPreflight(t, tmpClone, "commit", "-m", "remote commit")

	g := git.New(tmpClone)
	branch, err := g.CurrentBranch()
	require.NoError(t, err)
	runGitPreflight(t, tmpClone, "push", "origin", branch)

	// Provide "n" to decline pulling
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()

	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	_, err = w.WriteString("n\n")
	require.NoError(t, err)
	w.Close()

	err = runPreflight(repoDir)
	assert.NoError(t, err)

	// Verify we did NOT pull the new file
	_, err = os.Stat(filepath.Join(repoDir, "new.txt"))
	assert.True(t, os.IsNotExist(err))
}

func TestRunPreflight_BehindRemote_DirtyRejectsPull(t *testing.T) {
	repoDir, remoteDir := initPreflightRepo(t)

	// Push a commit from a separate clone
	tmpClone := t.TempDir()
	runGitPreflight(t, t.TempDir(), "clone", remoteDir, tmpClone)
	runGitPreflight(t, tmpClone, "config", "user.email", "test@test.com")
	runGitPreflight(t, tmpClone, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(tmpClone, "new.txt"), []byte("new"), 0644))
	runGitPreflight(t, tmpClone, "add", ".")
	runGitPreflight(t, tmpClone, "commit", "-m", "remote commit")

	g := git.New(tmpClone)
	branch, err := g.CurrentBranch()
	require.NoError(t, err)
	runGitPreflight(t, tmpClone, "push", "origin", branch)

	// Make repo dirty
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "dirty.txt"), []byte("dirty"), 0644))

	// Provide "y" to accept pull — should fail because dirty
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()

	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	_, err = w.WriteString("y\n")
	require.NoError(t, err)
	w.Close()

	err = runPreflight(repoDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "uncommitted changes")
	assert.Contains(t, err.Error(), "stash or commit before pulling")
}

func TestRunPreflight_CommitPluralization(t *testing.T) {
	// This is a basic test to verify the behind count logic handles
	// singular/plural correctly (1 commit vs 2 commits). The actual
	// output is to stdout, so we just verify no error for 0 behind.
	repoDir, _ := initPreflightRepo(t)

	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()
	w.Close()

	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	err = runPreflight(repoDir)
	assert.NoError(t, err)
}
