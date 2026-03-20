package claude

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxDirTreeChars    = 2000
	maxFileChars       = 2000
	maxGitLogChars     = 1000
	maxMilestonesChars = 1000
)

// excludedDirs are directories excluded from the directory tree.
var excludedDirs = map[string]bool{
	".git": true, "node_modules": true, "dist": true,
	"vendor": true, "bin": true,
}

// gatherDirTree returns a 2-level-deep directory listing of repoRoot.
// Excludes directories in excludedDirs. Returns empty string on error.
func gatherDirTree(repoRoot string) string {
	entries, err := os.ReadDir(repoRoot)
	if err != nil {
		return ""
	}

	// Separate dirs and files, filtering excluded dirs.
	var dirs, files []string
	for _, e := range entries {
		if e.IsDir() {
			if excludedDirs[e.Name()] {
				continue
			}
			dirs = append(dirs, e.Name())
		} else {
			files = append(files, e.Name())
		}
	}
	sort.Strings(dirs)
	sort.Strings(files)

	var b strings.Builder
	for _, d := range dirs {
		fmt.Fprintf(&b, "%s/\n", d)
		children, err := os.ReadDir(filepath.Join(repoRoot, d))
		if err != nil {
			continue
		}
		var childDirs, childFiles []string
		for _, c := range children {
			if c.IsDir() {
				if excludedDirs[c.Name()] {
					continue
				}
				childDirs = append(childDirs, c.Name())
			} else {
				childFiles = append(childFiles, c.Name())
			}
		}
		sort.Strings(childDirs)
		sort.Strings(childFiles)
		for _, cd := range childDirs {
			fmt.Fprintf(&b, "  %s/\n", cd)
		}
		for _, cf := range childFiles {
			fmt.Fprintf(&b, "  %s\n", cf)
		}
	}
	for _, f := range files {
		fmt.Fprintf(&b, "%s\n", f)
	}

	return truncate(b.String(), maxDirTreeChars)
}

// gatherKeyFile reads a file relative to repoRoot, truncates to maxChars.
// Returns empty string if the file doesn't exist or can't be read.
func gatherKeyFile(repoRoot, relPath string, maxChars int) string {
	data, err := os.ReadFile(filepath.Join(repoRoot, relPath))
	if err != nil {
		return ""
	}
	return truncate(string(data), maxChars)
}

// gatherGitLog returns the last 10 commit messages in one-line format.
// Returns empty string on error.
func gatherGitLog(repoRoot string) string {
	cmd := exec.Command("git", "-C", repoRoot, "log", "--oneline", "-10")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return truncate(string(output), maxGitLogChars)
}

// detectManifestFile checks for common manifest files and returns the first
// one that exists, or empty string if none are found.
func detectManifestFile(repoRoot string) string {
	for _, name := range []string{"go.mod", "package.json", "Cargo.toml"} {
		if _, err := os.Stat(filepath.Join(repoRoot, name)); err == nil {
			return name
		}
	}
	return ""
}

// truncate truncates s to maxChars runes, appending "\n... (truncated)" if needed.
// It operates on runes rather than bytes to avoid splitting multi-byte UTF-8 characters.
func truncate(s string, maxChars int) string {
	runes := []rune(s)
	if len(runes) <= maxChars {
		return s
	}
	return string(runes[:maxChars]) + "\n... (truncated)"
}
