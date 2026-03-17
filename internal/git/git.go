package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// DiffStat returns the --stat output between two refs.
func (g *Git) DiffStat(base, head string) (string, error) {
	return g.output("diff", "--stat", base, head)
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

// IsMerging returns true if a merge is in progress (MERGE_HEAD exists).
func (g *Git) IsMerging() bool {
	_, err := os.Stat(filepath.Join(g.WorkDir, ".git", "MERGE_HEAD"))
	return err == nil
}

func (g *Git) HasConflicts() bool {
	err := g.run("diff", "--check")
	return err != nil
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
