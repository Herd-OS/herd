package planner

import (
	"context"
	"fmt"

	"github.com/herd-os/herd/internal/platform"
	ghplatform "github.com/herd-os/herd/internal/platform/github"
)

// maxMilestoneNameAttempts is the maximum number of titles tried
// (the original plus 9 suffixed variants " (2)" through " (10)").
const maxMilestoneNameAttempts = 10

// createMilestoneWithUniqueName tries to create a milestone with the given
// base title. On an "already_exists" conflict it retries with a numeric
// suffix (" (2)", " (3)", ..., " (10)"). Returns the created milestone and
// the title that was actually used. Non-conflict errors are returned
// immediately without retry. If every attempt conflicts, the original
// already_exists error from the first attempt is returned.
func createMilestoneWithUniqueName(ctx context.Context, p platform.Platform, baseTitle string) (*platform.Milestone, string, error) {
	var firstConflictErr error
	for attempt := 1; attempt <= maxMilestoneNameAttempts; attempt++ {
		title := baseTitle
		if attempt > 1 {
			title = fmt.Sprintf("%s (%d)", baseTitle, attempt)
		}
		ms, err := p.Milestones().Create(ctx, title, "", nil)
		if err == nil {
			return ms, title, nil
		}
		if !ghplatform.IsMilestoneAlreadyExists(err) {
			return nil, "", err
		}
		if firstConflictErr == nil {
			firstConflictErr = err
		}
	}
	return nil, "", firstConflictErr
}
