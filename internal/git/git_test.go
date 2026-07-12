package git

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

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func TestConfigureIdentity(t *testing.T) {
	// Create a repo without user identity
	dir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	g := New(dir)

	// Should set identity
	require.NoError(t, g.ConfigureIdentity("HerdOS Integrator", "herd@herd-os.com"))

	// Verify
	name, err := g.output("config", "user.name")
	require.NoError(t, err)
	assert.Equal(t, "HerdOS Integrator", name)

	email, err := g.output("config", "user.email")
	require.NoError(t, err)
	assert.Equal(t, "herd@herd-os.com", email)
}

func TestConfigureIdentity_DoesNotOverwrite(t *testing.T) {
	dir := initTestRepo(t) // already has user.name="Test", user.email="test@test.com"
	g := New(dir)

	require.NoError(t, g.ConfigureIdentity("HerdOS Integrator", "herd@herd-os.com"))

	// Should keep existing values
	name, err := g.output("config", "user.name")
	require.NoError(t, err)
	assert.Equal(t, "Test", name)

	email, err := g.output("config", "user.email")
	require.NoError(t, err)
	assert.Equal(t, "test@test.com", email)
}

func TestGitErrorsRedactCommandScopedAuthConfig(t *testing.T) {
	token := "ghs_secret_installation_token"
	err := CloneWithConfig("https://example.invalid/repo.git", filepath.Join(t.TempDir(), "repo"),
		"http.https://github.com/.extraHeader=Authorization: Bearer "+token)

	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "clone https://example.invalid/repo.git")
	assert.NotContains(t, msg, "-c")
	assert.NotContains(t, msg, "extraHeader")
	assert.NotContains(t, msg, token)
	assert.NotContains(t, strings.ToLower(msg), "authorization: bearer")
}

func TestCommandScopedAuthConfigDoesNotAppearInGitArgv(t *testing.T) {
	binDir := t.TempDir()
	capturePath := filepath.Join(t.TempDir(), "argv.txt")
	fakeGit := filepath.Join(binDir, "git")
	script := "#!/bin/sh\n" +
		"for arg do printf '<%s>' \"$arg\"; done > \"$HERD_GIT_ARGV_CAPTURE\"\n" +
		"exit 1\n"
	require.NoError(t, os.WriteFile(fakeGit, []byte(script), 0700))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HERD_GIT_ARGV_CAPTURE", capturePath)
	token := "ghs_secret_installation_token"
	credential := "eC1hY2Nlc3MtdG9rZW46" + token

	err := CloneWithConfig("https://github.com/acme/widgets.git", filepath.Join(t.TempDir(), "repo"),
		"http.https://github.com/.extraHeader=Authorization: Bearer "+token,
		"credential.helper=!f() { echo password="+token+"; }; f")

	require.Error(t, err)
	argv := string(mustReadFile(t, capturePath))
	assert.NotContains(t, argv, token)
	assert.NotContains(t, argv, credential)
	assert.NotContains(t, strings.ToLower(argv), "authorization")
	assert.NotContains(t, strings.ToLower(argv), "bearer")
	assert.NotContains(t, strings.ToLower(argv), "basic")
	assert.NotContains(t, strings.ToLower(argv), "x-access-token")
	assert.NotContains(t, strings.ToLower(argv), "password")
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

func TestDiffPathsDetectsPureRename(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	defaultBranch, err := g.CurrentBranch()
	require.NoError(t, err)

	require.NoError(t, g.CreateBranch("feature", defaultBranch))
	cmd := exec.Command("git", "mv", "README.md", "README-renamed.md")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "commit", "-m", "rename readme")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	diff, err := g.DiffPaths(defaultBranch, "feature", "README-renamed.md", "README.md")
	require.NoError(t, err)
	assert.Contains(t, diff, "rename from README.md")
	assert.Contains(t, diff, "rename to README-renamed.md")
}

func TestDiffStat(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	defaultBranch, err := g.CurrentBranch()
	require.NoError(t, err)

	// Create feature branch with a new file.
	require.NoError(t, g.CreateBranch("feature", defaultBranch))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new"), 0644))
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "commit", "-m", "add new")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	stat, err := g.DiffStat(defaultBranch, "feature")
	require.NoError(t, err)
	assert.Contains(t, stat, "new.txt")
}

func TestDiffStat_ThreeDotSemantics(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	defaultBranch, err := g.CurrentBranch()
	require.NoError(t, err)

	// Create feature branch with a new file.
	require.NoError(t, g.CreateBranch("feature", defaultBranch))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature"), 0644))
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "commit", "-m", "add feature")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	// Add a separate commit on default branch so the branches diverge.
	require.NoError(t, g.Checkout(defaultBranch))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.txt"), []byte("main"), 0644))
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "commit", "-m", "add main")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	// Three-dot diff should show only the feature branch's changes,
	// NOT the default branch's main.txt.
	stat, err := g.DiffStat(defaultBranch, "feature")
	require.NoError(t, err)
	assert.Contains(t, stat, "feature.txt")
	assert.NotContains(t, stat, "main.txt")
}

func TestBinaryDiffAndApplyBinaryPatch(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)
	base, err := g.HeadSHA()
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "binary.bin"), []byte{0x00, 0x01, 0xfe, 0xff}, 0644))
	require.NoError(t, g.Add("."))
	require.NoError(t, g.Commit("add binary"))
	head, err := g.HeadSHA()
	require.NoError(t, err)

	diff, err := g.BinaryDiff(base, head)
	require.NoError(t, err)
	assert.Contains(t, string(diff), "GIT binary patch")

	applyDir := initTestRepo(t)
	patchFile := filepath.Join(t.TempDir(), "change.patch")
	require.NoError(t, os.WriteFile(patchFile, diff, 0600))
	applyGit := New(applyDir)
	require.NoError(t, applyGit.ApplyBinaryPatch(patchFile))
	require.NoError(t, applyGit.Commit("apply patch"))
	assert.Equal(t, []byte{0x00, 0x01, 0xfe, 0xff}, mustReadFile(t, filepath.Join(applyDir, "binary.bin")))
}

func TestCloneRemoteBranchSHAAndPushHEAD(t *testing.T) {
	source := initTestRepo(t)
	sourceGit := New(source)
	defaultBranch, err := sourceGit.CurrentBranch()
	require.NoError(t, err)
	base, err := sourceGit.HeadSHA()
	require.NoError(t, err)

	remote := filepath.Join(t.TempDir(), "remote.git")
	cmd := exec.Command("git", "init", "--bare", remote)
	require.NoError(t, cmd.Run())
	runGit(t, source, "remote", "add", "origin", remote)
	require.NoError(t, sourceGit.Push("origin", defaultBranch))

	clone := filepath.Join(t.TempDir(), "clone")
	require.NoError(t, Clone(remote, clone))
	cloneGit := New(clone)
	gotBase, err := cloneGit.RemoteBranchSHA("origin", defaultBranch)
	require.NoError(t, err)
	assert.Equal(t, base, gotBase)

	require.NoError(t, cloneGit.CheckoutDetached(base))
	require.NoError(t, os.WriteFile(filepath.Join(clone, "new.txt"), []byte("new\n"), 0644))
	require.NoError(t, cloneGit.Add("."))
	require.NoError(t, cloneGit.ConfigureIdentity("App", "app@example.com"))
	require.NoError(t, cloneGit.Commit("new commit"))
	require.NoError(t, cloneGit.PushHEAD("origin", defaultBranch, base))

	updated, err := sourceGit.output("ls-remote", remote, "refs/heads/"+defaultBranch)
	require.NoError(t, err)
	assert.Contains(t, updated, "refs/heads/"+defaultBranch)
	assert.NotContains(t, updated, base)
}

func TestParseNameStatus(t *testing.T) {
	raw := strings.Join([]string{
		"A\tadded.go",
		"M\tmodified.go",
		"D\tdeleted.go",
		"R091\told.go\tnew.go",
		"C100\tsource.go\tcopy.go",
		"T\ttypechange",
		"X\tunknown.txt",
	}, "\n")

	got := parseNameStatus(raw)

	assert.Equal(t, []NameStatusEntry{
		{Status: "A", Path: "added.go"},
		{Status: "M", Path: "modified.go"},
		{Status: "D", Path: "deleted.go"},
		{Status: "R091", Path: "new.go", OldPath: "old.go"},
		{Status: "C100", Path: "copy.go", OldPath: "source.go"},
		{Status: "T", Path: "typechange"},
		{Status: "X", Path: "unknown.txt"},
	}, got)
}

func TestParseNameStatus_EmptyOutput(t *testing.T) {
	assert.Empty(t, parseNameStatus(""))
}

func TestParseNumStat(t *testing.T) {
	raw := strings.Join([]string{
		"2\t1\tmodified.go",
		"-\t-\timage.png",
		"0\t0\told.go => renamed.go",
		"3\t4\tpkg/{old.go => new.go}",
	}, "\n")

	got, err := parseNumStat(raw)
	require.NoError(t, err)

	assert.Equal(t, [2]int{2, 1}, got["modified.go"])
	assert.Equal(t, [2]int{-1, -1}, got["image.png"])
	assert.Equal(t, [2]int{0, 0}, got["renamed.go"])
	assert.Equal(t, [2]int{3, 4}, got["pkg/new.go"])
	assert.Equal(t, [2]int{3, 4}, got["pkg/{old.go => new.go}"])
}

func TestParseNumStat_InvalidLine(t *testing.T) {
	_, err := parseNumStat("not-enough-fields")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected 3 tab-separated fields")
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

func TestIsMerging(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	// Clean repo — not merging
	assert.False(t, g.IsMerging())

	// Create a merge conflict to enter merge state
	defaultBranch, err := g.CurrentBranch()
	require.NoError(t, err)

	require.NoError(t, g.CreateBranch("conflict", defaultBranch))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("conflict content"), 0644))
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "commit", "-m", "conflict change")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	require.NoError(t, g.Checkout(defaultBranch))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("main content"), 0644))
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "commit", "-m", "main change")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	// Merge fails — now in merge state
	err = g.Merge("conflict")
	require.Error(t, err)
	assert.True(t, g.IsMerging())

	// Abort merge — no longer merging
	require.NoError(t, g.AbortMerge())
	assert.False(t, g.IsMerging())
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestIsDirty_CleanRepo(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	dirty, err := g.IsDirty()
	require.NoError(t, err)
	assert.False(t, dirty)
}

func TestIsDirty_UntrackedFile(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("hello"), 0644))

	dirty, err := g.IsDirty()
	require.NoError(t, err)
	assert.True(t, dirty)
}

func TestIsDirty_StagedFile(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "staged.txt"), []byte("hello"), 0644))
	runGit(t, dir, "add", "staged.txt")

	dirty, err := g.IsDirty()
	require.NoError(t, err)
	assert.True(t, dirty)
}

func TestIsDirty_ModifiedTrackedFile(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("modified"), 0644))

	dirty, err := g.IsDirty()
	require.NoError(t, err)
	assert.True(t, dirty)
}

func TestBehindCount_UpToDate(t *testing.T) {
	// Create a bare remote and clone it
	bareDir := t.TempDir()
	runGit(t, bareDir, "init", "--bare")

	cloneDir := t.TempDir()
	cmd := exec.Command("git", "clone", bareDir, cloneDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	// Configure identity and create initial commit in clone
	runGit(t, cloneDir, "config", "user.email", "test@test.com")
	runGit(t, cloneDir, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "file.txt"), []byte("init"), 0644))
	runGit(t, cloneDir, "add", ".")
	runGit(t, cloneDir, "commit", "-m", "init")
	runGit(t, cloneDir, "push", "origin", "HEAD")

	g := New(cloneDir)
	runGit(t, cloneDir, "fetch", "origin")

	branch, berr := g.CurrentBranch()
	require.NoError(t, berr)

	count, err := g.BehindCount("origin", branch)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestBehindCount_Behind(t *testing.T) {
	// Create a bare remote and clone it twice
	bareDir := t.TempDir()
	runGit(t, bareDir, "init", "--bare")

	clone1 := t.TempDir()
	cmd := exec.Command("git", "clone", bareDir, clone1)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	runGit(t, clone1, "config", "user.email", "test@test.com")
	runGit(t, clone1, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(clone1, "file.txt"), []byte("init"), 0644))
	runGit(t, clone1, "add", ".")
	runGit(t, clone1, "commit", "-m", "init")
	runGit(t, clone1, "push", "origin", "HEAD")

	clone2 := t.TempDir()
	cmd = exec.Command("git", "clone", bareDir, clone2)
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	runGit(t, clone2, "config", "user.email", "test@test.com")
	runGit(t, clone2, "config", "user.name", "Test")

	// Add 3 commits to clone1 and push
	for i := 0; i < 3; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(clone1, "file.txt"), []byte(fmt.Sprintf("v%d", i)), 0644))
		runGit(t, clone1, "add", ".")
		runGit(t, clone1, "commit", "-m", fmt.Sprintf("commit %d", i))
	}
	runGit(t, clone1, "push", "origin", "HEAD")

	// Fetch in clone2 and check behind count
	g := New(clone2)
	runGit(t, clone2, "fetch", "origin")

	branch, berr := New(clone1).CurrentBranch()
	require.NoError(t, berr)

	count, err := g.BehindCount("origin", branch)
	require.NoError(t, err)
	assert.Equal(t, 3, count)
}

func TestDefaultBranch_WithOriginHEAD(t *testing.T) {
	// Create a bare remote with a commit on main
	bareDir := t.TempDir()
	runGit(t, bareDir, "init", "--bare")

	// Create a temporary repo to push an initial commit
	tmpDir := t.TempDir()
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "checkout", "-b", "main")
	runGit(t, tmpDir, "config", "user.email", "test@test.com")
	runGit(t, tmpDir, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("init"), 0644))
	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "init")
	runGit(t, tmpDir, "remote", "add", "origin", bareDir)
	runGit(t, tmpDir, "push", "-u", "origin", "main")

	// Clone — git clone sets origin/HEAD automatically
	cloneDir := t.TempDir()
	cmd := exec.Command("git", "clone", bareDir, cloneDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	g := New(cloneDir)
	branch, err := g.DefaultBranch()
	require.NoError(t, err)
	assert.Equal(t, "main", branch)
}

func TestDefaultBranch_FallbackToMain(t *testing.T) {
	// Create a bare remote with a commit on main
	bareDir := t.TempDir()
	runGit(t, bareDir, "init", "--bare")

	tmpDir := t.TempDir()
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "checkout", "-b", "main")
	runGit(t, tmpDir, "config", "user.email", "test@test.com")
	runGit(t, tmpDir, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("init"), 0644))
	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "init")
	runGit(t, tmpDir, "remote", "add", "origin", bareDir)
	runGit(t, tmpDir, "push", "-u", "origin", "main")

	// Set up a local repo with origin but no origin/HEAD
	localDir := t.TempDir()
	runGit(t, localDir, "init")
	runGit(t, localDir, "checkout", "-b", "main")
	runGit(t, localDir, "config", "user.email", "test@test.com")
	runGit(t, localDir, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(localDir, "file.txt"), []byte("init"), 0644))
	runGit(t, localDir, "add", ".")
	runGit(t, localDir, "commit", "-m", "init")
	runGit(t, localDir, "remote", "add", "origin", bareDir)
	runGit(t, localDir, "fetch", "origin")

	// Remove origin/HEAD if it exists
	cmd := exec.Command("git", "remote", "set-head", "origin", "-d")
	cmd.Dir = localDir
	_ = cmd.Run() // ignore error if HEAD wasn't set

	g := New(localDir)
	branch, err := g.DefaultBranch()
	require.NoError(t, err)
	assert.Equal(t, "main", branch)
}

func TestDeleteLocalBranch(t *testing.T) {
	work := initTestRepo(t)
	g := New(work)

	defaultBranch, err := g.CurrentBranch()
	require.NoError(t, err)

	// Create a branch to delete
	require.NoError(t, g.CreateBranch("to-delete", defaultBranch))
	require.NoError(t, g.Checkout(defaultBranch))

	// Delete it
	require.NoError(t, g.DeleteLocalBranch("to-delete"))

	// Verify it's gone — checkout should fail
	err = g.Checkout("to-delete")
	assert.Error(t, err)
}

func TestDeleteLocalBranch_NonExistent(t *testing.T) {
	work := initTestRepo(t)
	g := New(work)
	err := g.DeleteLocalBranch("nonexistent")
	assert.Error(t, err)
}

func TestAbortMerge_NoMergeInProgress(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	err := g.AbortMerge()
	assert.Error(t, err)
}

func TestAbortRebase_NoRebaseInProgress(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	err := g.AbortRebase()
	assert.Error(t, err)
}

func TestAbortRebase_DuringRebase(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	defaultBranch, err := g.CurrentBranch()
	require.NoError(t, err)

	// Create feature branch with conflicting change
	require.NoError(t, g.CreateBranch("feature", defaultBranch))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("feature content"), 0644))
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "commit", "-m", "feature change")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	// Add conflicting commit on default branch
	require.NoError(t, g.Checkout(defaultBranch))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("main content"), 0644))
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "commit", "-m", "main change")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	// Rebase feature onto default — should fail with conflict
	require.NoError(t, g.Checkout("feature"))
	err = g.Rebase(defaultBranch)
	require.Error(t, err)

	// Verify rebase is in progress
	_, statErr := os.Stat(filepath.Join(dir, ".git", "rebase-merge"))
	assert.NoError(t, statErr, "rebase-merge directory should exist during rebase")

	// Abort should succeed
	require.NoError(t, g.AbortRebase())

	// Verify rebase is no longer in progress
	_, statErr = os.Stat(filepath.Join(dir, ".git", "rebase-merge"))
	assert.True(t, os.IsNotExist(statErr), "rebase-merge should not exist after abort")
}

func TestResetHead_UndoesStagedChanges(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	// Stage a new file
	require.NoError(t, os.WriteFile(filepath.Join(dir, "staged.txt"), []byte("hello"), 0644))
	runGit(t, dir, "add", "staged.txt")

	dirty, err := g.IsDirty()
	require.NoError(t, err)
	assert.True(t, dirty, "should be dirty after staging")

	// Reset HEAD to unstage
	require.NoError(t, g.ResetHead())

	// File should still exist but be untracked, not staged
	// Check that there are no staged changes (only untracked)
	out, err := g.output("diff", "--cached", "--name-only")
	require.NoError(t, err)
	assert.Empty(t, out, "no staged changes after ResetHead")
}

func TestResetHead_UndoesStagedRemoval(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	// Stage removal of README.md
	require.NoError(t, g.Rm("README.md"))

	// Verify it's staged
	out, err := g.output("diff", "--cached", "--name-only")
	require.NoError(t, err)
	assert.Contains(t, out, "README.md")

	// Reset HEAD to undo the staged removal
	require.NoError(t, g.ResetHead())

	// No staged changes
	out, err = g.output("diff", "--cached", "--name-only")
	require.NoError(t, err)
	assert.Empty(t, out, "no staged changes after ResetHead")
}

func TestResetHead_CleanRepo(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	// ResetHead on a clean repo should succeed (no-op)
	require.NoError(t, g.ResetHead())
}

func TestRevParse(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	sha, err := g.RevParse("HEAD")
	require.NoError(t, err)
	assert.Len(t, sha, 40)

	// Non-existent ref should error
	_, err = g.RevParse("refs/heads/nonexistent")
	assert.Error(t, err)
}

func TestMergeBase(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	defaultBranch, err := g.CurrentBranch()
	require.NoError(t, err)

	// Record the initial commit SHA (will be the merge base)
	baseSHA, err := g.RevParse("HEAD")
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

	// Add a commit on default branch
	require.NoError(t, g.Checkout(defaultBranch))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.txt"), []byte("main"), 0644))
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "commit", "-m", "add main")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	// Merge base should be the initial commit
	mb, err := g.MergeBase(defaultBranch, "feature")
	require.NoError(t, err)
	assert.Equal(t, baseSHA, mb)
}

func TestMergeBase_AncestorBranch(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	defaultBranch, err := g.CurrentBranch()
	require.NoError(t, err)

	// Create feature branch (no extra commits — same as default)
	require.NoError(t, g.CreateBranch("feature", defaultBranch))

	featureSHA, err := g.RevParse("feature")
	require.NoError(t, err)

	// When feature is ancestor of default (or same), merge-base == feature tip
	mb, err := g.MergeBase(defaultBranch, "feature")
	require.NoError(t, err)
	assert.Equal(t, featureSHA, mb)
}

func TestHasConflicts_WithConflict(t *testing.T) {
	dir := initTestRepo(t)
	g := New(dir)

	defaultBranch, err := g.CurrentBranch()
	require.NoError(t, err)

	// Create conflicting branches
	require.NoError(t, g.CreateBranch("feature", defaultBranch))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("feature content"), 0644))
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "commit", "-m", "feature change")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	require.NoError(t, g.Checkout(defaultBranch))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("main content"), 0644))
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "commit", "-m", "main change")
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	// Merge to create conflict markers
	_ = g.Merge("feature")
	assert.True(t, g.HasConflicts())

	// Cleanup
	_ = g.AbortMerge()
}
