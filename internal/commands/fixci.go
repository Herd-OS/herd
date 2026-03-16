package commands

import (
	"context"
	"fmt"
	"regexp"
	"strconv"

	"github.com/herd-os/herd/internal/integrator"
)

func init() {
	Register("fix-ci", handleFixCI)
}

var batchBranchRe = regexp.MustCompile(`^herd/batch/(\d+)-`)

func handleFixCI(ctx context.Context, hctx *HandlerContext, cmd *Command) (string, error) {
	if hctx.PRNumber == 0 {
		return "", fmt.Errorf("fix-ci can only be used on batch PRs")
	}

	pr, err := hctx.Platform.PullRequests().Get(ctx, hctx.PRNumber)
	if err != nil {
		return "", fmt.Errorf("getting PR #%d: %w", hctx.PRNumber, err)
	}

	m := batchBranchRe.FindStringSubmatch(pr.Head)
	if m == nil {
		return "", fmt.Errorf("fix-ci can only be used on batch PRs")
	}

	batchNum, err := strconv.Atoi(m[1])
	if err != nil {
		return "", fmt.Errorf("parsing batch number: %w", err)
	}

	params := integrator.CheckCIParams{
		BatchNumber: batchNum,
	}
	if cmd.Prompt != "" {
		params.UserContext = "User context: " + cmd.Prompt
	}

	result, err := integrator.CheckCI(ctx, hctx.Platform, hctx.Config, params)
	if err != nil {
		return "", fmt.Errorf("checking CI: %w", err)
	}

	batchBranch := pr.Head

	switch {
	case result.Status == "success":
		return fmt.Sprintf("✅ CI is passing on `%s`.", batchBranch), nil
	case result.Status == "pending":
		return fmt.Sprintf("⏳ CI is still running on `%s`.", batchBranch), nil
	case result.MaxCyclesHit:
		return "⚠️ CI is failing but max fix cycles reached. Manual intervention needed.", nil
	default:
		if len(result.FixIssues) > 0 {
			return fmt.Sprintf("🔧 CI is failing. Dispatched fix worker: #%d (cycle %d).", result.FixIssues[0], result.FixCycle), nil
		}
		return "⚠️ CI is failing but no fix worker was dispatched. Check logs for errors.", nil
	}
}
