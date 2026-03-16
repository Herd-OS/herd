package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/herd-os/herd/internal/agent"
	"github.com/herd-os/herd/internal/agent/claude"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/git"
	"github.com/herd-os/herd/internal/integrator"
	"github.com/herd-os/herd/internal/platform"
)

func init() {
	Register("review", handleReview)
}

// reviewFn is the function used to run the agent review. It can be replaced in tests.
var reviewFn = func(ctx context.Context, p platform.Platform, ag agent.Agent, g *git.Git, cfg *config.Config, params integrator.ReviewParams) (*integrator.ReviewResult, error) {
	return integrator.Review(ctx, p, ag, g, cfg, params)
}

func handleReview(ctx context.Context, hctx *HandlerContext, cmd *Command) (string, error) {
	if hctx.PRNumber == 0 {
		return "", fmt.Errorf("review can only be used on batch PRs")
	}

	pr, err := hctx.Platform.PullRequests().Get(ctx, hctx.PRNumber)
	if err != nil {
		return "", fmt.Errorf("getting PR #%d: %w", hctx.PRNumber, err)
	}

	if !strings.HasPrefix(pr.Head, "herd/batch/") {
		return "", fmt.Errorf("review can only be used on batch PRs")
	}

	ag := claude.New(hctx.Config.Agent.Binary, hctx.Config.Agent.Model)
	g := git.New(hctx.RepoRoot)

	params := integrator.ReviewParams{
		PRNumber: hctx.PRNumber,
		RepoRoot: hctx.RepoRoot,
	}

	if cmd.Prompt != "" {
		ri, _ := os.ReadFile(filepath.Join(hctx.RepoRoot, ".herd", "integrator.md"))
		params.SystemPrompt = string(ri) + "\n\n## Additional Review Instructions\n\n" + cmd.Prompt
	}

	result, err := reviewFn(ctx, hctx.Platform, ag, g, hctx.Config, params)
	if err != nil {
		return "", fmt.Errorf("running review: %w", err)
	}

	switch {
	case result.Approved:
		return "✅ **Agent Review**: All acceptance criteria met. LGTM.", nil
	case result.MaxCyclesHit:
		return "⚠️ **Agent Review**: Found issues but max fix cycles reached.", nil
	default:
		nums := make([]string, len(result.FixIssues))
		for i, n := range result.FixIssues {
			nums[i] = fmt.Sprintf("#%d", n)
		}
		return fmt.Sprintf("🔍 **Agent Review**: Found issues. Dispatched fix workers: %s (cycle %d).",
			strings.Join(nums, ", "), result.FixCycle), nil
	}
}
