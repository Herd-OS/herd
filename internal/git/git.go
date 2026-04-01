package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Git wraps the git CLI for repository operations.
type Git struct {
	WorkDir string
}

// New creates a new Git instance for the given working directory.
func New(workDir string) *Git {
	return &Git{WorkDir: workDir}
}

// ConfigureIdentity sets the local git user identity for commits/merges if not already set.
func (g *Git) ConfigureIdentity(name, email string) error {
	// Check if local config already has identity (ignore global config)
	if _, err := g.output("config", "--local", "user.name"); err != nil {
		if err := g.run("config", "user.name", name); err != nil {
			return err
		}
	}
	if _, err := g.output("config", "--local", "user.email"); err != nil {
		if err := g.run("config", "user.email", email); err != nil {
			return err
		}
	}
	return nil
}

func (g *Git) Checkout(branch string) error {
	return g.run("checkout", branch)
}

// CheckoutReset checks out a branch, resetting it to match the remote tracking branch.
// This ensures the local branch is up-to-date even if the remote has advanced.
func (g *Git) CheckoutReset(branch string) error {
	return g.run("checkout", "-B", branch, "origin/"+branch)
}

func (g *Git) CreateBranch(name, from string) error {
	return g.run("checkout", "-b", name, from)
}

func (g *Git) Fetch(remote string) error {
	return g.run("fetch", remote)
}

func (g *Git) Merge(branch string) error {
	return g.run("merge", branch)
}

func (g *Git) Rebase(onto string) error {
	return g.run("rebase", onto)
}

func (g *Git) Push(remote, branch string) error {
	return g.run("push", remote, branch)
}

func (g *Git) ForcePush(remote, branch string) error {
	return g.run("push", "--force-with-lease", remote, branch)
}

func (g *Git) Pull(remote, branch string) error {
	return g.run("pull", remote, branch)
}

func (g *Git) Diff(base, head string) (string, error) {
	return g.output("diff", base+"..."+head)
}

// DiffStat returns the --stat output for changes introduced by head since
// its merge base with base (three-dot diff), matching the semantics of Diff.
func (g *Git) DiffStat(base, head string) (string, error) {
	return g.output("diff", "--stat", base+"..."+head)
}

func (g *Git) CurrentBranch() (string, error) {
	return g.output("rev-parse", "--abbrev-ref", "HEAD")
}

func (g *Git) HeadSHA() (string, error) {
	return g.output("rev-parse", "HEAD")
}

// AbortMerge aborts an in-progress merge.
func (g *Git) AbortMerge() error {
	return g.run("merge", "--abort")
}

// AbortRebase aborts an in-progress rebase.
func (g *Git) AbortRebase() error {
	return g.run("rebase", "--abort")
}

// DeleteLocalBranch force-deletes a local branch.
func (g *Git) DeleteLocalBranch(name string) error {
	return g.run("branch", "-D", name)
}

// Rm removes a file from the git index and working tree.
func (g *Git) Rm(path string) error {
	return g.run("rm", path)
}

// RmDir removes a directory recursively from the git index and working tree.
// Returns nil if the path does not exist in the index.
func (g *Git) RmDir(path string) error {
	return g.run("rm", "-r", "--ignore-unmatch", path)
}

// Commit creates a new commit with the given message.
func (g *Git) Commit(message string) error {
	return g.run("commit", "-m", message)
}

// AmendNoEdit amends the most recent commit without changing its message.
func (g *Git) AmendNoEdit() error {
	return g.run("commit", "--amend", "--no-edit")
}

// ResetHead resets the index to match HEAD, undoing any staged changes.
func (g *Git) ResetHead() error {
	return g.run("reset", "HEAD")
}

// IsMerging returns true if a merge is in progress (MERGE_HEAD exists).
func (g *Git) IsMerging() bool {
	_, err := os.Stat(filepath.Join(g.WorkDir, ".git", "MERGE_HEAD"))
	return err == nil
}

func (g *Git) HasConflicts() bool {
	err := g.run("diff", "--check")
	return err != nil
}

// IsDirty returns true if the working tree has uncommitted changes
// (staged or unstaged, including untracked files).
func (g *Git) IsDirty() (bool, error) {
	out, err := g.output("status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// BehindCount returns the number of commits HEAD is behind remote/branch.
// Caller must fetch before calling this.
func (g *Git) BehindCount(remote, branch string) (int, error) {
	out, err := g.output("rev-list", "--count", "HEAD.."+remote+"/"+branch)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(out)
	if err != nil {
		return 0, fmt.Errorf("parsing behind count: %w", err)
	}
	return n, nil
}

// DefaultBranch returns the default branch name by inspecting origin/HEAD.
// Falls back to "main" if origin/HEAD is not set.
func (g *Git) DefaultBranch() (string, error) {
	out, err := g.output("symbolic-ref", "refs/remotes/origin/HEAD")
	if err != nil {
		// Fallback: origin/HEAD not set, try common defaults
		if _, checkErr := g.output("rev-parse", "--verify", "refs/remotes/origin/main"); checkErr == nil {
			return "main", nil
		}
		if _, checkErr := g.output("rev-parse", "--verify", "refs/remotes/origin/master"); checkErr == nil {
			return "master", nil
		}
		return "", fmt.Errorf("cannot determine default branch: %w", err)
	}
	// out is like "refs/remotes/origin/main"
	parts := strings.SplitN(out, "refs/remotes/origin/", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("unexpected symbolic-ref output: %s", out)
	}
	return parts[1], nil
}

// MergeBase returns the merge base commit SHA between two refs.
func (g *Git) MergeBase(a, b string) (string, error) {
	return g.output("merge-base", a, b)
}

// RevParse returns the SHA for a ref.
func (g *Git) RevParse(ref string) (string, error) {
	return g.output("rev-parse", ref)
}

func (g *Git) run(args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = g.WorkDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (g *Git) output(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = g.WorkDir
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}
