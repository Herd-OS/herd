package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/herd-os/herd/internal/display"
	"github.com/herd-os/herd/internal/git"
)

// runPreflight checks the git repo state and interactively prompts the user
// to switch branches and/or pull latest changes.
func runPreflight(dir string) error {
	g := git.New(dir)
	scanner := bufio.NewScanner(os.Stdin)

	// Step 1: Always fetch silently
	fmt.Print(display.InProgress("Fetching from origin..."))
	if err := g.Fetch("origin"); err != nil {
		fmt.Println()
		fmt.Println(display.Warning(fmt.Sprintf("Failed to fetch: %v (continuing with local state)", err)))
	} else {
		fmt.Print("\r" + display.Success("Fetched from origin") + "    \n")
	}

	// Step 2: Determine default branch and current branch
	defaultBranch, err := g.DefaultBranch()
	if err != nil {
		fmt.Println(display.Warning(fmt.Sprintf("Could not determine default branch: %v", err)))
		// Can't do branch checks, skip to dirty check
		return preflightDirtyCheck(g)
	}

	currentBranch, err := g.CurrentBranch()
	if err != nil {
		return fmt.Errorf("getting current branch: %w", err)
	}

	// Step 3: If on a different branch, offer to switch
	if currentBranch != defaultBranch {
		fmt.Printf("%s Switch to '%s' and pull latest? [Y/n] ",
			display.Warning(fmt.Sprintf("You're on branch '%s'.", currentBranch)),
			defaultBranch)

		if scanner.Scan() {
			response := strings.TrimSpace(strings.ToLower(scanner.Text()))
			if response == "" || response == "y" || response == "yes" {
				// Check dirty before switching
				dirty, err := g.IsDirty()
				if err != nil {
					return fmt.Errorf("checking working tree: %w", err)
				}
				if dirty {
					return fmt.Errorf("working tree has uncommitted changes — please stash or commit before switching branches")
				}

				if err := g.Checkout(defaultBranch); err != nil {
					return fmt.Errorf("switching to %s: %w", defaultBranch, err)
				}
				if err := g.Pull("origin", defaultBranch); err != nil {
					fmt.Println(display.Warning(fmt.Sprintf("Pull failed: %v", err)))
				} else {
					fmt.Println(display.Success(fmt.Sprintf("Switched to '%s' and pulled latest", defaultBranch)))
				}
				return nil // Already pulled, done
			}
		}
		// User declined switch — fall through to behind check on current branch
	}

	// Step 4: Check if behind remote
	branch := currentBranch

	behind, err := g.BehindCount("origin", branch)
	if err != nil {
		// Non-fatal — remote tracking might not exist for this branch
		fmt.Println(display.Warning("Could not check remote sync status"))
	} else if behind > 0 {
		commitWord := "commit"
		if behind != 1 {
			commitWord = "commits"
		}
		fmt.Printf("%s Pull latest? [Y/n] ",
			display.Warning(fmt.Sprintf("Local is %d %s behind origin/%s.", behind, commitWord, branch)))

		if scanner.Scan() {
			response := strings.TrimSpace(strings.ToLower(scanner.Text()))
			if response == "" || response == "y" || response == "yes" {
				// Check dirty before pulling
				dirty, err := g.IsDirty()
				if err != nil {
					return fmt.Errorf("checking working tree: %w", err)
				}
				if dirty {
					return fmt.Errorf("working tree has uncommitted changes — please stash or commit before pulling")
				}

				if err := g.Pull("origin", branch); err != nil {
					fmt.Println(display.Warning(fmt.Sprintf("Pull failed: %v", err)))
				} else {
					fmt.Println(display.Success("Pulled latest changes"))
				}
			} else {
				fmt.Println(display.Warning("Continuing with local state"))
			}
		}
	}

	// Step 5: Informational dirty warning
	return preflightDirtyCheck(g)
}

// preflightDirtyCheck prints an informational warning if the working tree is dirty.
func preflightDirtyCheck(g *git.Git) error {
	dirty, err := g.IsDirty()
	if err != nil {
		return fmt.Errorf("checking working tree: %w", err)
	}
	if dirty {
		fmt.Println(display.Warning("Working tree has uncommitted changes. The planner will see your local state."))
	}
	return nil
}
