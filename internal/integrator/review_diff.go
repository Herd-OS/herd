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
	summary := preparedCoverageSummary(prepared)
	return strings.TrimRight(comment, "\n") + "\n\n" + summary
}

func logDiffCoverageIfLimited(prepared reviewdiff.PreparedDiff) {
	if !prepared.Rendered.WasLimited {
		return
	}
	fmt.Print(preparedCoverageSummary(prepared))
}

func preparedCoverageSummary(prepared reviewdiff.PreparedDiff) string {
	if prepared.Coverage.TotalFiles > 0 || len(prepared.Chunks) > 0 {
		return reviewdiff.FormatChunkedCoverageSummary(reviewdiff.ChunkPlan{
			DiffSet:  prepared.DiffSet,
			Chunks:   prepared.Chunks,
			Coverage: prepared.Coverage,
		}, len(prepared.Chunks), reviewdiff.DefaultMaxOmittedSummaryEntries)
	}
	return reviewdiff.FormatCoverageSummary(
		prepared.DiffSet,
		prepared.Rendered,
		reviewdiff.DefaultMaxOmittedSummaryEntries,
	)
}
