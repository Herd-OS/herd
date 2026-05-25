package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/herd-os/herd/internal/display"
)

// knownFlavors are the valid runner flavors (base = minimal herd-runner-base).
var knownFlavors = []string{"node", "ruby", "python", "go", "base"}

func isKnownFlavor(f string) bool {
	for _, k := range knownFlavors {
		if k == f {
			return true
		}
	}
	return false
}

// detectRunnerFlavor sniffs manifest files in dir and returns the chosen flavor
// plus the list of manifests that matched (for the multi-manifest note).
// Priority (first match wins): go.mod->go, Gemfile->ruby, package.json->node,
// (requirements.txt|pyproject.toml|setup.py)->python, else "base".
func detectRunnerFlavor(dir string) (flavor string, matched []string) {
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	type rule struct {
		flavor string
		files  []string
	}
	rules := []rule{
		{"go", []string{"go.mod"}},
		{"ruby", []string{"Gemfile"}},
		{"node", []string{"package.json"}},
		{"python", []string{"requirements.txt", "pyproject.toml", "setup.py"}},
	}
	chosen := ""
	for _, r := range rules {
		for _, f := range r.files {
			if has(f) {
				if chosen == "" {
					chosen = r.flavor
				}
				matched = append(matched, f)
			}
		}
	}
	if chosen == "" {
		return "base", matched
	}
	return chosen, matched
}

// resolveRunnerFlavor picks the effective runner flavor. A non-empty override is
// validated against knownFlavors and wins over detection; otherwise the flavor is
// detected from manifest files in dir. An informational message is printed in the
// detection path.
func resolveRunnerFlavor(dir, override string) (string, error) {
	if override != "" {
		if !isKnownFlavor(override) {
			return "", fmt.Errorf("unknown runner flavor %q — valid values: %s", override, strings.Join(knownFlavors, ", "))
		}
		return override, nil
	}
	flavor, matched := detectRunnerFlavor(dir)
	switch {
	case flavor == "base":
		fmt.Println(display.Success("No recognized manifest — using minimal herd-runner-base."))
	case len(matched) > 1:
		fmt.Println(display.Success(fmt.Sprintf("Multiple manifests detected (%s) — using herd-runner-%s. Add other toolchains in Dockerfile.herd_runner.", strings.Join(matched, ", "), flavor)))
	default:
		fmt.Println(display.Success(fmt.Sprintf("Detected %s project — using herd-runner-%s base image.", flavorDisplayName(flavor), flavor)))
	}
	return flavor, nil
}

func flavorDisplayName(f string) string {
	switch f {
	case "go":
		return "Go"
	case "node":
		return "Node"
	case "ruby":
		return "Ruby"
	case "python":
		return "Python"
	default:
		return f
	}
}
