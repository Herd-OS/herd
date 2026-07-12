package reviewdiff

import (
	"fmt"
	"sort"
	"strings"
)

type chunkedCoverageSummaryMode int

const (
	chunkedCoverageSummaryModeReviewed chunkedCoverageSummaryMode = iota
	chunkedCoverageSummaryModeInteractivePrompt
)

func FormatChunkedCoverageSummary(plan ChunkPlan, chunksReviewed int, maxEntries int) string {
	return formatChunkedCoverageSummary(plan, chunksReviewed, maxEntries, chunkedCoverageSummaryModeReviewed)
}

func FormatInteractivePromptCoverageSummary(plan ChunkPlan, chunksIncluded int, maxEntries int) string {
	return formatChunkedCoverageSummary(plan, chunksIncluded, maxEntries, chunkedCoverageSummaryModeInteractivePrompt)
}

func formatChunkedCoverageSummary(plan ChunkPlan, chunksCount int, maxEntries int, mode chunkedCoverageSummaryMode) string {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxOmittedSummaryEntries
	}
	coverage := plan.Coverage
	if chunksCount < 0 {
		chunksCount = 0
	}
	if chunksCount > len(plan.Chunks) {
		chunksCount = len(plan.Chunks)
	}
	coverage.ChunksReviewed = chunksCount
	chunksPlanned := coverage.ChunksPlanned
	if coverage.RequiredChunks > chunksPlanned {
		chunksPlanned = coverage.RequiredChunks
	}
	complete := coverage.Complete && chunksCount >= chunksPlanned
	if mode == chunkedCoverageSummaryModeInteractivePrompt {
		complete = coverage.Complete
	}

	var b strings.Builder
	b.WriteString("## Diff Coverage\n\n")
	fmt.Fprintf(&b, "- Source: %s\n", valueOrUnknown(coverage.Source))
	fmt.Fprintf(&b, "- Review mode: %s\n", coverage.ReviewMode)
	if mode == chunkedCoverageSummaryModeInteractivePrompt {
		fmt.Fprintf(&b, "- Chunks included in this prompt: %d/%d\n", chunksCount, chunksPlanned)
		fmt.Fprintf(&b, "- PR-level planned review chunks: %d\n", chunksPlanned)
		b.WriteString("- PR-level file coverage:\n")
		fmt.Fprintf(&b, "  - Total files: %d\n", coverage.TotalFiles)
		fmt.Fprintf(&b, "  - Files included in planned review chunks: %d\n", coverage.FilesReviewed)
		fmt.Fprintf(&b, "  - Files included with truncated diffs: %d\n", coverage.FilesReviewedWithTruncatedDiffs)
		fmt.Fprintf(&b, "  - Files summarized but not included in review chunks: %d\n", coverage.FilesSummarizedNotReviewed)
		fmt.Fprintf(&b, "  - Files not included in review chunks: %d\n", coverage.FilesNotReviewed)
	} else {
		fmt.Fprintf(&b, "- Total files: %d\n", coverage.TotalFiles)
		fmt.Fprintf(&b, "- Chunks reviewed: %d/%d\n", chunksCount, chunksPlanned)
		fmt.Fprintf(&b, "- Files reviewed: %d\n", coverage.FilesReviewed)
		fmt.Fprintf(&b, "- Files reviewed with truncated diffs: %d\n", coverage.FilesReviewedWithTruncatedDiffs)
		fmt.Fprintf(&b, "- Files summarized but not reviewed: %d\n", coverage.FilesSummarizedNotReviewed)
		fmt.Fprintf(&b, "- Files not reviewed: %d\n", coverage.FilesNotReviewed)
	}
	if complete {
		if mode == chunkedCoverageSummaryModeInteractivePrompt {
			b.WriteString("- PR-level planned file coverage is complete; all reviewable files were assigned to review chunks.\n")
		} else {
			b.WriteString("- Review coverage is complete; all reviewable files were reviewed.\n")
		}
	} else {
		reason := coverage.PartialReason
		if reason == "" {
			reason = "not all files were reviewed"
		}
		if mode == chunkedCoverageSummaryModeInteractivePrompt {
			fmt.Fprintf(&b, "- PR-level planned file coverage is partial: %s.\n", reason)
		} else {
			fmt.Fprintf(&b, "- This is a partial review: %s.\n", reason)
		}
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
		if mode == chunkedCoverageSummaryModeInteractivePrompt {
			b.WriteString("- PR-level planned chunks:\n")
		} else {
			b.WriteString("- Chunks:\n")
		}
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
