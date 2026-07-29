package integrator

import (
	"fmt"

	"github.com/herd-os/herd/internal/issues"
	"github.com/herd-os/herd/internal/platform"
)

type HostedReviewSkipInput struct {
	PRNumber    int
	BatchNumber int
	HeadSHA     string
	Labels      []string
	Comments    []*platform.Comment
	Manual      bool
}

type HostedReviewSkipDecision struct {
	Skip   bool
	Status string
	Reason string
}

func HostedReviewReadOnlySkipDecision(input HostedReviewSkipInput) HostedReviewSkipDecision {
	if input.Manual {
		return HostedReviewSkipDecision{}
	}
	if marker, ok := latestReviewResultMarker(input.Comments, input.PRNumber, input.BatchNumber, input.HeadSHA); ok && marker.Status == reviewResultStatusApproved {
		return HostedReviewSkipDecision{
			Skip:   true,
			Status: reviewResultStatusApproved,
			Reason: duplicateApprovedReviewSkipReason(input.PRNumber, input.HeadSHA),
		}
	}
	if issues.HasLabel(input.Labels, issues.StableDisagreement) {
		return HostedReviewSkipDecision{
			Skip:   true,
			Status: "failed",
			Reason: fmt.Sprintf("Skipping review: PR #%d has %s label.", input.PRNumber, issues.StableDisagreement),
		}
	}
	return HostedReviewSkipDecision{}
}
