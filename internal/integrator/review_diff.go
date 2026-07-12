package integrator

import (
	"fmt"
	"strings"

	"github.com/herd-os/herd/internal/reviewdiff"
)

func appendDiffCoverageIfLimited(comment string, prepared reviewdiff.PreparedDiff) string {
	if !prepared.Rendered.WasLimited {
		return comment
	}
	summary := reviewdiff.FormatCoverageSummary(
		prepared.DiffSet,
		prepared.Rendered,
		reviewdiff.DefaultMaxOmittedSummaryEntries,
	)
	return strings.TrimRight(comment, "\n") + "\n\n" + summary
}

func logDiffCoverageIfLimited(prepared reviewdiff.PreparedDiff) {
	if !prepared.Rendered.WasLimited {
		return
	}
	fmt.Print(reviewdiff.FormatCoverageSummary(
		prepared.DiffSet,
		prepared.Rendered,
		reviewdiff.DefaultMaxOmittedSummaryEntries,
	))
}
