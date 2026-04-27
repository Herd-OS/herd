package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"text/template"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/agent/claude"
	"github.com/herd-os/herd/internal/platform"
	"github.com/spf13/cobra"
)

func newReviewCmd() *cobra.Command {
	var initialPrompt string
	cmd := &cobra.Command{
		Use:   "review <pr-number>",
		Short: "Open an interactive agent session scoped to a PR",
		Long: "Launch an interactive Claude Code session pre-loaded with a PR's " +
			"diff, comments, and CI status. The agent acts as a reviewer/fixer " +
			"assistant — you drive the conversation; it can read code, discuss " +
			"findings, and make changes if you ask. It will NOT auto-dispatch " +
			"workers or create issues.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := parsePRArg(args[0])
			if err != nil {
				return err
			}
			return runReview(cmd.Context(), n, initialPrompt)
		},
	}
	cmd.Flags().StringVarP(&initialPrompt, "prompt", "p", "", "Optional initial message to send to the agent")
	return cmd
}

func parsePRArg(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid PR number: %q", s)
	}
	return n, nil
}

func runReview(ctx context.Context, prNumber int, initialPrompt string) error {
	cfg, err := loadConfigOrExit()
	if err != nil {
		return err
	}

	client, err := newClientOrExit(cfg.Platform.Owner, cfg.Platform.Repo)
	if err != nil {
		return err
	}

	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	data, err := buildReviewPromptData(ctx, client, prNumber)
	if err != nil {
		return err
	}

	if ri, rerr := os.ReadFile(filepath.Join(dir, ".herd", "integrator.md")); rerr == nil {
		data.RoleInstructions = string(ri)
	}

	systemPrompt, err := renderReviewSystemPrompt(data)
	if err != nil {
		return err
	}

	ag := claude.New(cfg.Agent.Binary, cfg.Agent.Model)
	return ag.Discuss(ctx, agent.DiscussOptions{
		RepoRoot:      dir,
		SystemPrompt:  systemPrompt,
		InitialPrompt: initialPrompt,
	})
}

func buildReviewPromptData(ctx context.Context, client platform.Platform, prNumber int) (*reviewCmdPromptData, error) {
	pr, err := client.PullRequests().Get(ctx, prNumber)
	if err != nil {
		return nil, fmt.Errorf("getting PR #%d: %w", prNumber, err)
	}
	diff, err := client.PullRequests().GetDiff(ctx, prNumber)
	if err != nil {
		return nil, fmt.Errorf("getting PR diff: %w", err)
	}

	var general []reviewCmdComment
	if cs, cerr := client.Issues().ListComments(ctx, prNumber); cerr == nil {
		for _, c := range cs {
			general = append(general, reviewCmdComment{Author: c.AuthorLogin, Body: c.Body})
		}
	} else {
		fmt.Fprintf(os.Stderr, "warning: failed to list PR comments: %v\n", cerr)
	}

	var inline []reviewCmdInlineComment
	if rcs, rerr := client.PullRequests().ListReviewComments(ctx, prNumber); rerr == nil {
		for _, c := range rcs {
			inline = append(inline, reviewCmdInlineComment{
				Author:   c.AuthorLogin,
				Path:     c.Path,
				Line:     c.Line,
				DiffHunk: c.DiffHunk,
				Body:     c.Body,
			})
		}
	} else {
		fmt.Fprintf(os.Stderr, "warning: failed to list inline review comments: %v\n", rerr)
	}

	ciStatus := "unknown"
	if s, serr := client.Checks().GetCombinedStatus(ctx, pr.Head); serr == nil {
		ciStatus = s
	} else {
		fmt.Fprintf(os.Stderr, "warning: failed to get CI status: %v\n", serr)
	}

	return &reviewCmdPromptData{
		PRNumber:       prNumber,
		PRTitle:        pr.Title,
		PRURL:          pr.URL,
		PRBaseBranch:   pr.Base,
		PRHeadBranch:   pr.Head,
		Diff:           diff,
		Comments:       general,
		InlineComments: inline,
		CIStatus:       ciStatus,
	}, nil
}

type reviewCmdPromptData struct {
	PRNumber         int
	PRTitle          string
	PRURL            string
	PRBaseBranch     string
	PRHeadBranch     string
	Diff             string
	Comments         []reviewCmdComment
	InlineComments   []reviewCmdInlineComment
	CIStatus         string
	RoleInstructions string
}

type reviewCmdComment struct {
	Author string
	Body   string
}

type reviewCmdInlineComment struct {
	Author   string
	Path     string
	Line     int
	DiffHunk string
	Body     string
}

const reviewSystemPromptTemplate = `You are a HerdOS PR review/fix assistant. The user has opened an interactive session scoped to a single pull request and wants to discuss and potentially fix issues on it.

## Pull Request #{{.PRNumber}}: {{.PRTitle}}
URL: {{.PRURL}}
Base: {{.PRBaseBranch}}
Head: {{.PRHeadBranch}}
CI status (head ref): {{.CIStatus}}

## Diff
` + "```diff\n{{.Diff}}\n```" + `
{{if .Comments}}
## PR Conversation Comments
{{range .Comments}}
---
**@{{.Author}}:**
{{.Body}}
{{end}}
{{end}}
{{if .InlineComments}}
## Inline Review Comments (line-level)
{{range .InlineComments}}
---
**@{{.Author}}** on ` + "`{{.Path}}`" + ` line {{.Line}}:
` + "```diff\n{{.DiffHunk}}\n```" + `
{{.Body}}
{{end}}
{{end}}
## Your Role

You are a reviewer/fixer assistant for this PR. The user drives the conversation. You can:
- Read the codebase to investigate findings
- Discuss the diff, the comments, and the CI status with the user
- Make code changes ONLY if the user explicitly asks you to

You MUST NOT:
- Automatically dispatch workers, create GitHub issues, or post comments on the PR
- Take action on findings without the user's go-ahead
- Treat this session as a planning session (no JSON output, no batch creation)

If the user asks about CI failure logs, fetch them on demand using ` + "`gh run view --log-failed`" + ` or similar — only the CI status (success/failure/pending) is included above; full logs are not in this prompt to keep it small.
{{if .RoleInstructions}}
## Project-Specific Reviewer Instructions (.herd/integrator.md)
{{.RoleInstructions}}
{{end}}`

func renderReviewSystemPrompt(d *reviewCmdPromptData) (string, error) {
	tmpl, err := template.New("review-cmd").Parse(reviewSystemPromptTemplate)
	if err != nil {
		return "", fmt.Errorf("parsing review prompt template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, d); err != nil {
		return "", fmt.Errorf("executing review prompt template: %w", err)
	}
	return buf.String(), nil
}
