package reviewdiff

import (
	"fmt"
	"sort"
	"strings"
)

func FormatChunkedCoverageSummary(plan ChunkPlan, chunksReviewed int, maxEntries int) string {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxOmittedSummaryEntries
	}
	coverage := plan.Coverage
	if chunksReviewed < 0 {
		chunksReviewed = 0
	}
	if chunksReviewed > len(plan.Chunks) {
		chunksReviewed = len(plan.Chunks)
	}
	coverage.ChunksReviewed = chunksReviewed
	complete := coverage.Complete && chunksReviewed >= coverage.ChunksPlanned

	var b strings.Builder
	b.WriteString("## Diff Coverage\n\n")
	fmt.Fprintf(&b, "- Source: %s\n", valueOrUnknown(coverage.Source))
	fmt.Fprintf(&b, "- Review mode: %s\n", coverage.ReviewMode)
	fmt.Fprintf(&b, "- Total files: %d\n", coverage.TotalFiles)
	fmt.Fprintf(&b, "- Chunks reviewed: %d/%d\n", chunksReviewed, coverage.ChunksPlanned)
	fmt.Fprintf(&b, "- Files reviewed: %d\n", coverage.FilesReviewed)
	fmt.Fprintf(&b, "- Files reviewed with truncated diffs: %d\n", coverage.FilesReviewedWithTruncatedDiffs)
	fmt.Fprintf(&b, "- Files summarized but not reviewed: %d\n", coverage.FilesSummarizedNotReviewed)
	fmt.Fprintf(&b, "- Files not reviewed: %d\n", coverage.FilesNotReviewed)
	if complete {
		b.WriteString("- Review coverage is complete; all reviewable files were reviewed.\n")
	} else {
		reason := coverage.PartialReason
		if reason == "" {
			reason = "not all files were reviewed"
		}
		fmt.Fprintf(&b, "- This is a partial review: %s.\n", reason)
	}
	if coverage.ExceededMaxChunks {
		fmt.Fprintf(&b, "- Required chunks: %d; max chunks: %d\n", coverage.RequiredChunks, coverage.MaxChunks)
	}
	if len(coverage.Warnings) > 0 {
		b.WriteString("- Warnings:\n")
		for _, warning := range coverage.Warnings {
			fmt.Fprintf(&b, "  - %s\n", warning)
		}
	}
	if len(plan.Chunks) > 0 {
		b.WriteString("- Chunks:\n")
		for _, chunk := range plan.Chunks {
			fmt.Fprintf(&b, "  - Chunk %d/%d: %d files, %s\n", chunk.Index, chunk.Total, len(chunk.IncludedFiles), formatBytes(chunk.UsedDiffBytes))
		}
	}
	if len(coverage.OmittedByReason) > 0 {
		b.WriteString("- Files not reviewed by reason:\n")
		for _, reason := range sortedReasons(coverage.OmittedByReason) {
			fmt.Fprintf(&b, "  - %s: %d\n", reason, coverage.OmittedByReason[reason])
			writeCoverageExamples(&b, coverage.NotReviewedFiles, reason, maxEntries)
		}
	}
	if len(coverage.TruncatedFiles) > 0 {
		b.WriteString("- Truncated reviewed path examples:\n")
		limit := min(maxEntries, len(coverage.TruncatedFiles))
		for _, file := range coverage.TruncatedFiles[:limit] {
			fmt.Fprintf(&b, "  - %s: %s\n", file.Path, valueOrUnknown(file.Reason))
		}
		if len(coverage.TruncatedFiles) > limit {
			fmt.Fprintf(&b, "  - ... %d additional truncated files not shown\n", len(coverage.TruncatedFiles)-limit)
		}
	}
	return b.String()
}

func sortedReasons(reasons map[string]int) []string {
	keys := make([]string, 0, len(reasons))
	for reason := range reasons {
		keys = append(keys, reason)
	}
	sort.Strings(keys)
	return keys
}

func writeCoverageExamples(b *strings.Builder, files []FileCoverage, reason string, maxEntries int) {
	written := 0
	total := 0
	for _, file := range files {
		if file.Reason != reason {
			continue
		}
		total++
		if written >= maxEntries {
			continue
		}
		fmt.Fprintf(b, "    - %s\n", file.Path)
		written++
	}
	if total > written {
		fmt.Fprintf(b, "    - ... %d additional files not shown\n", total-written)
	}
}

func formatBytes(bytes int) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	kb := (bytes + 1023) / 1024
	return fmt.Sprintf("%d KB", kb)
}
