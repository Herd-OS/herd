package issues

import (
	"fmt"
	"strings"
)

// FrontMatter holds the YAML front matter fields for a herd issue.
type FrontMatter struct {
	Version             int      `yaml:"version"`
	Batch               int      `yaml:"batch,omitempty"`
	DependsOn           []int    `yaml:"depends_on,omitempty"`
	Scope               []string `yaml:"scope,omitempty"`
	EstimatedComplexity string   `yaml:"estimated_complexity,omitempty"`
	RunnerLabel         string   `yaml:"runner_label,omitempty"`
	// Integrator-generated fields
	Type                string   `yaml:"type,omitempty"`
	FixCycle            int      `yaml:"fix_cycle,omitempty"`
	BatchPR             int      `yaml:"batch_pr,omitempty"`
	CIFixCycle          int      `yaml:"ci_fix_cycle,omitempty"`
	ConflictResolution  bool     `yaml:"conflict_resolution,omitempty"`
	ConflictingBranches []string `yaml:"conflicting_branches,omitempty"`
}

// IssueBody holds the parsed content of a herd issue body.
type IssueBody struct {
	FrontMatter           FrontMatter
	Task                  string
	ImplementationDetails string
	Conventions           []string
	ContextFromDeps       []string
	Criteria              []string
	Context               string
	ConversationHistory   string
	FilesToModify         []string
}

// RenderBody generates the full issue body from structured data.
func RenderBody(body IssueBody) string {
	var b strings.Builder

	// YAML front matter
	b.WriteString("---\nherd:\n")
	b.WriteString(fmt.Sprintf("  version: %d\n", body.FrontMatter.Version))
	if body.FrontMatter.Batch > 0 {
		b.WriteString(fmt.Sprintf("  batch: %d\n", body.FrontMatter.Batch))
	}
	if len(body.FrontMatter.DependsOn) > 0 {
		b.WriteString(fmt.Sprintf("  depends_on: %s\n", formatIntSlice(body.FrontMatter.DependsOn)))
	}
	if len(body.FrontMatter.Scope) > 0 {
		b.WriteString("  scope:\n")
		for _, s := range body.FrontMatter.Scope {
			b.WriteString(fmt.Sprintf("    - %s\n", s))
		}
	}
	if body.FrontMatter.EstimatedComplexity != "" {
		b.WriteString(fmt.Sprintf("  estimated_complexity: %s\n", body.FrontMatter.EstimatedComplexity))
	}
	if body.FrontMatter.RunnerLabel != "" {
		b.WriteString(fmt.Sprintf("  runner_label: %s\n", body.FrontMatter.RunnerLabel))
	}
	if body.FrontMatter.Type != "" {
		b.WriteString(fmt.Sprintf("  type: %s\n", body.FrontMatter.Type))
	}
	if body.FrontMatter.FixCycle > 0 {
		b.WriteString(fmt.Sprintf("  fix_cycle: %d\n", body.FrontMatter.FixCycle))
	}
	if body.FrontMatter.BatchPR > 0 {
		b.WriteString(fmt.Sprintf("  batch_pr: %d\n", body.FrontMatter.BatchPR))
	}
	if body.FrontMatter.CIFixCycle > 0 {
		b.WriteString(fmt.Sprintf("  ci_fix_cycle: %d\n", body.FrontMatter.CIFixCycle))
	}
	if body.FrontMatter.ConflictResolution {
		b.WriteString("  conflict_resolution: true\n")
		if len(body.FrontMatter.ConflictingBranches) > 0 {
			b.WriteString("  conflicting_branches:\n")
			for _, br := range body.FrontMatter.ConflictingBranches {
				b.WriteString(fmt.Sprintf("    - %s\n", br))
			}
		}
	}
	b.WriteString("---\n\n")

	// Task
	b.WriteString("## Task\n\n")
	b.WriteString(body.Task)
	b.WriteString("\n\n")

	// Implementation Details
	if body.ImplementationDetails != "" {
		b.WriteString("## Implementation Details\n\n")
		b.WriteString(body.ImplementationDetails)
		b.WriteString("\n\n")
	}

	// Conventions
	if len(body.Conventions) > 0 {
		b.WriteString("## Conventions\n\n")
		for _, c := range body.Conventions {
			b.WriteString(fmt.Sprintf("- %s\n", c))
		}
		b.WriteString("\n")
	}

	// Context from Dependencies
	if len(body.ContextFromDeps) > 0 {
		b.WriteString("## Context from Dependencies\n\n")
		for _, c := range body.ContextFromDeps {
			b.WriteString(fmt.Sprintf("- %s\n", c))
		}
		b.WriteString("\n")
	}

	// Acceptance Criteria
	if len(body.Criteria) > 0 {
		b.WriteString("## Acceptance Criteria\n\n")
		for _, c := range body.Criteria {
			b.WriteString(fmt.Sprintf("- [ ] %s\n", c))
		}
		b.WriteString("\n")
	}

	// Context
	if body.Context != "" {
		b.WriteString("## Context\n\n")
		b.WriteString(body.Context)
		b.WriteString("\n\n")
	}

	// Conversation History
	if body.ConversationHistory != "" {
		b.WriteString("## Conversation History\n\n")
		b.WriteString(body.ConversationHistory)
		b.WriteString("\n\n")
	}

	// Files to Modify
	if len(body.FilesToModify) > 0 {
		b.WriteString("## Files to Modify\n\n")
		for _, f := range body.FilesToModify {
			b.WriteString(fmt.Sprintf("- `%s`\n", f))
		}
		b.WriteString("\n")
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

func formatIntSlice(nums []int) string {
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = fmt.Sprintf("%d", n)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
