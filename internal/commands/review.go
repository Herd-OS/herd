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

	if _, err := reviewFn(ctx, hctx.Platform, ag, g, hctx.Config, params); err != nil {
		return "", fmt.Errorf("running review: %w", err)
	}

	// integrator.Review already posts a detailed comment to the PR on all paths.
	// Return "" to avoid a duplicate comment from the command handler.
	return "", nil
}
