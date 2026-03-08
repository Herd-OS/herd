package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		require.NoError(t, cmd.Run())
	}
	// Initial commit
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test"), 0644))
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	return dir
}

func TestCurrentBranch(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	branch, err := g.CurrentBranch()
	require.NoError(t, err)
	assert.Contains(t, []string{"main", "master"}, branch)
}

func TestHeadSHA(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	sha, err := g.HeadSHA()
	require.NoError(t, err)
	assert.Len(t, sha, 40)
}

func TestCreateBranchAndCheckout(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	defaultBranch, err := g.CurrentBranch()
	require.NoError(t, err)

	require.NoError(t, g.CreateBranch("feature", defaultBranch))

	branch, err := g.CurrentBranch()
	require.NoError(t, err)
	assert.Equal(t, "feature", branch)

	require.NoError(t, g.Checkout(defaultBranch))
	branch, err = g.CurrentBranch()
	require.NoError(t, err)
	assert.Equal(t, defaultBranch, branch)
}

func TestMerge(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	defaultBranch, err := g.CurrentBranch()
	require.NoError(t, err)

	// Create feature branch with a commit
	require.NoError(t, g.CreateBranch("feature", defaultBranch))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature"), 0644))
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "commit", "-m", "add feature")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	// Merge back to default branch
	require.NoError(t, g.Checkout(defaultBranch))
	require.NoError(t, g.Merge("feature"))

	// Verify file exists
	_, err = os.Stat(filepath.Join(dir, "feature.txt"))
	assert.NoError(t, err)
}

func TestDiff(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	defaultBranch, err := g.CurrentBranch()
	require.NoError(t, err)

	require.NoError(t, g.CreateBranch("feature", defaultBranch))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new"), 0644))
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "commit", "-m", "add new")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	diff, err := g.Diff(defaultBranch, "feature")
	require.NoError(t, err)
	assert.Contains(t, diff, "new.txt")
}

func TestRebase(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	defaultBranch, err := g.CurrentBranch()
	require.NoError(t, err)

	// Create feature branch with a commit
	require.NoError(t, g.CreateBranch("feature", defaultBranch))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature"), 0644))
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "commit", "-m", "add feature")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	// Add a commit to default branch
	require.NoError(t, g.Checkout(defaultBranch))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.txt"), []byte("main"), 0644))
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "commit", "-m", "add main")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	// Rebase feature onto default
	require.NoError(t, g.Checkout("feature"))
	require.NoError(t, g.Rebase(defaultBranch))

	// Both files should exist
	_, err = os.Stat(filepath.Join(dir, "feature.txt"))
	assert.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "main.txt"))
	assert.NoError(t, err)
}

func TestMergeConflict(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	defaultBranch, err := g.CurrentBranch()
	require.NoError(t, err)

	// Create feature branch modifying README
	require.NoError(t, g.CreateBranch("feature", defaultBranch))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("feature content"), 0644))
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "commit", "-m", "feature change")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	// Modify same file on default branch
	require.NoError(t, g.Checkout(defaultBranch))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("main content"), 0644))
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "commit", "-m", "main change")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	// Merge should fail with conflict
	err = g.Merge("feature")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "git merge")
}

func TestHasConflicts(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	// No conflicts in clean repo
	assert.False(t, g.HasConflicts())
}

func TestDiffNoBranch(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	_, err := g.Diff("nonexistent", "alsononexistent")
	assert.Error(t, err)
}

func TestCreateBranchFromNonexistent(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	err := g.CreateBranch("feature", "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "git checkout")
}

func TestCheckoutNonexistent(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	err := g.Checkout("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "git checkout")
}
