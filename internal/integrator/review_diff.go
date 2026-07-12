package integrator

import (
	"fmt"
	"strings"

	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/reviewdiff"
)

func chunkOptionsFromConfig(cfg *config.Config) reviewdiff.ChunkOptions {
	return reviewdiff.ChunkOptions{
		MaxChunkBytes:            cfg.Integrator.ReviewDiff.MaxChunkBytes,
		MaxFileDiffBytes:         cfg.Integrator.ReviewDiff.MaxFileBytes,
		MaxFilesPerChunk:         cfg.Integrator.ReviewDiff.MaxFilesPerChunk,
		MaxChunks:                cfg.Integrator.ReviewDiff.MaxChunks,
		MaxOmittedSummaryEntries: reviewdiff.DefaultMaxOmittedSummaryEntries,
	}
}

func appendDiffCoverageIfLimited(comment string, prepared reviewdiff.PreparedDiff) string {
	if !shouldAppendPreparedCoverage(prepared) {
		return comment
	}
	summary := preparedCoverageSummary(prepared)
	return strings.TrimRight(comment, "\n") + "\n\n" + summary
}

func logDiffCoverageIfLimited(prepared reviewdiff.PreparedDiff) {
	if !shouldAppendPreparedCoverage(prepared) {
		return
	}
	fmt.Print(preparedCoverageSummary(prepared))
}

func shouldAppendPreparedCoverage(prepared reviewdiff.PreparedDiff) bool {
	if prepared.Coverage.TotalFiles > 0 || len(prepared.Chunks) > 0 {
		return len(prepared.Chunks) > 1 ||
			!prepared.Coverage.Complete ||
			prepared.Coverage.FilesReviewedWithTruncatedDiffs > 0 ||
			prepared.Coverage.FilesSummarizedNotReviewed > 0 ||
			prepared.Coverage.FilesNotReviewed > 0 ||
			prepared.Coverage.ExceededMaxChunks
	}
	return prepared.Rendered.WasLimited
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
