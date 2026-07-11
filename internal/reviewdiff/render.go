package reviewdiff

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

func RenderForReview(diff DiffSet, opts RenderOptions) RenderResult {
	opts = normalizeOptions(opts)
	var result RenderResult
	result.Warnings = append(result.Warnings, diff.Warnings...)

	var body strings.Builder
	includedCount := 0
	usedBytes := 0

	for _, original := range diff.Files {
		file := MarkGeneratedAndLarge(original, opts)
		patchBytes := len(file.Patch)

		switch {
		case file.Binary:
			file.Omitted = true
			file.OmitReason = firstReason(file.OmitReason, "binary file")
		case file.Generated:
			file.Omitted = true
			file.OmitReason = firstReason(file.OmitReason, "generated file")
		case isLargeLockfileChange(file):
			file.Omitted = true
			file.OmitReason = firstReason(file.OmitReason, "large lockfile diff")
		case includedCount >= opts.MaxIncludedFiles:
			file.Omitted = true
			file.OmitReason = firstReason(file.OmitReason, "max included file count reached")
		case usedBytes >= opts.MaxTotalDiffBytes:
			file.Omitted = true
			file.OmitReason = firstReason(file.OmitReason, "total diff byte limit reached")
		case patchBytes == 0:
			if isModeOnly(file) {
				file.Omitted = true
				file.OmitReason = firstReason(file.OmitReason, "mode-only change")
			}
		case patchBytes > opts.MaxFileDiffBytes:
			file.Truncated = true
			file.OmitReason = firstReason(file.OmitReason, "per-file diff byte limit reached")
		}

		if file.Omitted {
			result.OmittedFiles = append(result.OmittedFiles, file)
			result.WasLimited = true
			continue
		}

		if file.Truncated {
			remaining := opts.MaxTotalDiffBytes - usedBytes
			if remaining <= 0 {
				file.Truncated = false
				file.Omitted = true
				file.OmitReason = "total diff byte limit reached"
				result.OmittedFiles = append(result.OmittedFiles, file)
				result.WasLimited = true
				continue
			}
			limit := min(opts.MaxFileDiffBytes, remaining)
			if limit < patchBytes {
				result.TruncatedFiles = append(result.TruncatedFiles, file)
				result.WasLimited = true
			}
			writeFilePatch(&body, file, truncateBytes(file.Patch, limit), true)
			usedBytes += min(limit, patchBytes)
			includedCount++
			result.IncludedFiles = append(result.IncludedFiles, file)
			continue
		}

		if usedBytes+patchBytes > opts.MaxTotalDiffBytes {
			file.Omitted = true
			file.OmitReason = "total diff byte limit reached"
			result.OmittedFiles = append(result.OmittedFiles, file)
			result.WasLimited = true
			continue
		}

		writeFilePatch(&body, file, file.Patch, false)
		usedBytes += patchBytes
		includedCount++
		result.IncludedFiles = append(result.IncludedFiles, file)
	}

	appendLimitWarnings(&result)
	result.Text = renderHeader(diff, result.Warnings) + body.String() + renderOmittedSummary(result.OmittedFiles, opts.MaxOmittedSummaryEntries)
	return result
}

func renderHeader(diff DiffSet, warnings []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Review diff\n\n")
	fmt.Fprintf(&b, "- Source: %s\n", valueOrUnknown(diff.Source))
	if diff.PRNumber != 0 {
		fmt.Fprintf(&b, "- PR: #%d\n", diff.PRNumber)
	}
	fmt.Fprintf(&b, "- Base SHA: %s\n", valueOrUnknown(diff.BaseSHA))
	fmt.Fprintf(&b, "- Head SHA: %s\n", valueOrUnknown(diff.HeadSHA))
	fmt.Fprintf(&b, "- Total files: %d\n", len(diff.Files))
	for _, warning := range warnings {
		fmt.Fprintf(&b, "- Warning: %s\n", warning)
	}
	b.WriteString("\n")
	return b.String()
}

func writeFilePatch(b *strings.Builder, file ChangedFile, patch string, truncated bool) {
	fmt.Fprintf(b, "## %s\n\n", fileTitle(file))
	fmt.Fprintf(b, "- Status: %s\n", statusOrUnknown(file.Status))
	if file.OldPath != "" && file.OldPath != file.Path {
		fmt.Fprintf(b, "- Old path: %s\n", file.OldPath)
	}
	fmt.Fprintf(b, "- Additions: %d\n", file.Additions)
	fmt.Fprintf(b, "- Deletions: %d\n", file.Deletions)
	if file.PreviousMode != "" || file.CurrentMode != "" {
		fmt.Fprintf(b, "- Mode: %s -> %s\n", valueOrUnknown(file.PreviousMode), valueOrUnknown(file.CurrentMode))
	}
	if truncated {
		fmt.Fprintf(b, "- Warning: truncated, reason: %s\n", file.OmitReason)
	}
	b.WriteString("\n```diff\n")
	b.WriteString(patch)
	if patch != "" && !strings.HasSuffix(patch, "\n") {
		b.WriteString("\n")
	}
	if truncated {
		b.WriteString("\n[diff truncated]\n")
	}
	b.WriteString("```\n\n")
}

func renderOmittedSummary(files []ChangedFile, maxEntries int) string {
	if len(files) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Omitted files\n\n")
	limit := min(maxEntries, len(files))
	for _, file := range files[:limit] {
		fmt.Fprintf(&b, "- %s", fileTitle(file))
		fmt.Fprintf(&b, " (%s", statusOrUnknown(file.Status))
		if file.OldPath != "" && file.OldPath != file.Path {
			fmt.Fprintf(&b, ", old path: %s", file.OldPath)
		}
		fmt.Fprintf(&b, ", +%d/-%d", file.Additions, file.Deletions)
		if file.PreviousMode != "" || file.CurrentMode != "" {
			fmt.Fprintf(&b, ", mode: %s -> %s", valueOrUnknown(file.PreviousMode), valueOrUnknown(file.CurrentMode))
		}
		fmt.Fprintf(&b, ", reason: %s", valueOrUnknown(file.OmitReason))
		b.WriteString(")\n")
	}
	if len(files) > limit {
		fmt.Fprintf(&b, "- ... %d additional omitted files not shown\n", len(files)-limit)
	}
	b.WriteString("\n")
	return b.String()
}

func appendLimitWarnings(result *RenderResult) {
	if len(result.OmittedFiles) > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%d omitted file(s); omitted summaries include generated, binary, large, mode-only, and limit reasons when present", len(result.OmittedFiles)))
	}
	if len(result.TruncatedFiles) > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%d truncated file diff(s) due to byte limits", len(result.TruncatedFiles)))
	}
	generated, binary := 0, 0
	for _, file := range result.OmittedFiles {
		if file.Generated {
			generated++
		}
		if file.Binary {
			binary++
		}
	}
	if generated > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%d generated file(s) summarized", generated))
	}
	if binary > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%d binary file(s) summarized", binary))
	}
}

func truncateBytes(s string, limit int) string {
	if limit <= 0 || limit >= len(s) {
		return s
	}
	s = s[:limit]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}

func isLargeLockfileChange(file ChangedFile) bool {
	diffBytes := len(file.Patch)
	return IsLargeLockfile(file.Path, diffBytes) || (file.OldPath != "" && IsLargeLockfile(file.OldPath, diffBytes))
}

func isModeOnly(file ChangedFile) bool {
	return (file.PreviousMode != "" || file.CurrentMode != "") && file.Additions == 0 && file.Deletions == 0 && file.Patch == ""
}

func fileTitle(file ChangedFile) string {
	if file.Path != "" {
		return file.Path
	}
	if file.OldPath != "" {
		return file.OldPath
	}
	return "(unknown path)"
}

func statusOrUnknown(status ChangeStatus) ChangeStatus {
	if status == "" {
		return ChangeUnknown
	}
	return status
}

func firstReason(existing, fallback string) string {
	if existing != "" {
		return existing
	}
	return fallback
}

func valueOrUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}
