package reviewdiff

import (
	"fmt"
	"strings"
)

type ChunkOptions struct {
	MaxChunkBytes            int
	MaxFileDiffBytes         int
	MaxFilesPerChunk         int
	MaxChunks                int
	MaxOmittedSummaryEntries int
}

type ReviewChunk struct {
	Index          int
	Total          int
	Text           string
	IncludedFiles  []ChangedFile
	OmittedFiles   []ChangedFile
	TruncatedFiles []ChangedFile
	Warnings       []string
	UsedDiffBytes  int
	WasLimited     bool
}

type CoverageMode string

const (
	CoverageModeFull    CoverageMode = "full"
	CoverageModeChunked CoverageMode = "chunked"
	CoverageModePartial CoverageMode = "partial"
)

type FileCoverage struct {
	File        ChangedFile
	Path        string
	Status      string
	Reviewed    bool
	Truncated   bool
	NotReviewed bool
	Reason      string
	ChunkIndex  int
}

type CoverageSummary struct {
	Source                          string
	TotalFiles                      int
	ReviewMode                      CoverageMode
	ChunksPlanned                   int
	ChunksReviewed                  int
	FilesReviewed                   int
	FilesReviewedWithTruncatedDiffs int
	FilesSummarizedNotReviewed      int
	FilesNotReviewed                int
	OmittedByReason                 map[string]int
	NotReviewedFiles                []FileCoverage
	ReviewedFiles                   []FileCoverage
	TruncatedFiles                  []FileCoverage
	Warnings                        []string
	Complete                        bool
	PartialReason                   string
	ExceededMaxChunks               bool
	RequiredChunks                  int
	MaxChunks                       int
}

type ChunkPlan struct {
	DiffSet  DiffSet
	Chunks   []ReviewChunk
	Coverage CoverageSummary
	Warnings []string
}

type plannedFile struct {
	file          ChangedFile
	reviewable    bool
	reviewed      bool
	truncated     bool
	notReviewed   bool
	reason        string
	chunkIndex    int
	usedDiffBytes int
}

func DefaultChunkOptions() ChunkOptions {
	return ChunkOptions{
		MaxChunkBytes:            DefaultMaxTotalDiffBytes,
		MaxFileDiffBytes:         DefaultMaxFileDiffBytes,
		MaxFilesPerChunk:         DefaultMaxIncludedFiles,
		MaxChunks:                8,
		MaxOmittedSummaryEntries: DefaultMaxOmittedSummaryEntries,
	}
}

func normalizeChunkOptions(opts ChunkOptions) ChunkOptions {
	defaults := DefaultChunkOptions()
	if opts.MaxChunkBytes <= 0 {
		opts.MaxChunkBytes = defaults.MaxChunkBytes
	}
	if opts.MaxFileDiffBytes <= 0 {
		opts.MaxFileDiffBytes = defaults.MaxFileDiffBytes
	}
	if opts.MaxFilesPerChunk <= 0 {
		opts.MaxFilesPerChunk = defaults.MaxFilesPerChunk
	}
	if opts.MaxChunks <= 0 {
		opts.MaxChunks = defaults.MaxChunks
	}
	if opts.MaxOmittedSummaryEntries <= 0 {
		opts.MaxOmittedSummaryEntries = defaults.MaxOmittedSummaryEntries
	}
	return opts
}

func ChunkForReview(diff DiffSet, opts ChunkOptions) ChunkPlan {
	opts = normalizeChunkOptions(opts)
	planned := make([]plannedFile, 0, len(diff.Files))
	requiredChunks := 0

	var chunks []ReviewChunk
	current := ReviewChunk{}

	flush := func() {
		if len(current.IncludedFiles) == 0 {
			return
		}
		requiredChunks++
		if len(chunks) < opts.MaxChunks {
			current.Index = len(chunks) + 1
			chunks = append(chunks, current)
		} else {
			for i := range planned {
				if planned[i].chunkIndex == requiredChunks && planned[i].reviewable && !planned[i].notReviewed {
					planned[i].reviewed = false
					planned[i].notReviewed = true
					planned[i].reviewable = false
					planned[i].reason = "max chunks reached"
					planned[i].chunkIndex = 0
					planned[i].truncated = false
					planned[i].file.Omitted = true
					planned[i].file.Truncated = false
					planned[i].file.OmitReason = planned[i].reason
				}
			}
		}
		current = ReviewChunk{}
	}

	for _, original := range diff.Files {
		file := MarkGeneratedAndLarge(original, RenderOptions{
			MaxFileDiffBytes:         opts.MaxFileDiffBytes,
			MaxTotalDiffBytes:        opts.MaxChunkBytes,
			MaxIncludedFiles:         opts.MaxFilesPerChunk,
			MaxOmittedSummaryEntries: opts.MaxOmittedSummaryEntries,
		})
		reviewable, reason := chunkReviewability(file)
		pf := plannedFile{file: file, reviewable: reviewable, reason: reason}
		planned = append(planned, pf)
		idx := len(planned) - 1
		if !reviewable {
			planned[idx].notReviewed = true
			planned[idx].file.Omitted = true
			planned[idx].file.OmitReason = reason
			continue
		}

		patchBytes := len(file.Patch)
		usedBytes := patchBytes
		if patchBytes > opts.MaxFileDiffBytes {
			usedBytes = opts.MaxFileDiffBytes
			planned[idx].truncated = true
			planned[idx].reason = "file diff exceeds per-file limit and was truncated"
			planned[idx].file.Truncated = true
			planned[idx].file.OmitReason = planned[idx].reason
		}
		if usedBytes > opts.MaxChunkBytes {
			planned[idx].reviewable = false
			planned[idx].notReviewed = true
			planned[idx].truncated = false
			planned[idx].reason = "file diff exceeds maximum reviewable size"
			planned[idx].file.Omitted = true
			planned[idx].file.Truncated = false
			planned[idx].file.OmitReason = planned[idx].reason
			continue
		}

		if len(current.IncludedFiles) > 0 && (len(current.IncludedFiles) >= opts.MaxFilesPerChunk || current.UsedDiffBytes+usedBytes > opts.MaxChunkBytes) {
			flush()
		}

		current.IncludedFiles = append(current.IncludedFiles, planned[idx].file)
		current.UsedDiffBytes += usedBytes
		planned[idx].reviewed = true
		planned[idx].chunkIndex = requiredChunks + 1
		planned[idx].usedDiffBytes = usedBytes
		if planned[idx].truncated {
			current.TruncatedFiles = append(current.TruncatedFiles, planned[idx].file)
			current.WasLimited = true
		}
	}
	flush()

	for i := range chunks {
		chunks[i].Total = max(len(chunks), requiredChunks)
		chunks[i].Warnings = append(chunks[i].Warnings, diff.Warnings...)
		if len(chunks[i].TruncatedFiles) > 0 {
			chunks[i].Warnings = append(chunks[i].Warnings, fmt.Sprintf("%d truncated file diff(s) in this chunk", len(chunks[i].TruncatedFiles)))
		}
		chunks[i].OmittedFiles = chunkOmittedFiles(planned, chunks[i].Index)
		if len(chunks[i].OmittedFiles) > 0 {
			chunks[i].WasLimited = true
		}
		chunks[i].Text = renderChunk(diff, chunks[i], opts)
	}

	coverage := buildCoverage(diff, chunks, planned, opts, requiredChunks)
	warnings := append([]string{}, coverage.Warnings...)
	return ChunkPlan{
		DiffSet:  diff,
		Chunks:   chunks,
		Coverage: coverage,
		Warnings: warnings,
	}
}

func chunkReviewability(file ChangedFile) (bool, string) {
	switch {
	case file.Omitted:
		return false, firstReason(file.OmitReason, "patch unavailable from source")
	case file.Binary:
		return false, "binary file"
	case file.Generated:
		return false, "generated file"
	case isLargeLockfileChange(file):
		return false, "large lockfile diff"
	case isModeOnly(file):
		return false, "mode-only change"
	default:
		return true, ""
	}
}

func chunkOmittedFiles(planned []plannedFile, chunkIndex int) []ChangedFile {
	omitted := make([]ChangedFile, 0)
	for _, pf := range planned {
		if pf.reviewed && pf.chunkIndex == chunkIndex {
			continue
		}
		file := pf.file
		file.Omitted = true
		file.Truncated = false
		if pf.reviewed {
			file.OmitReason = "reviewed in another chunk"
		} else {
			file.OmitReason = valueOrUnknown(pf.reason)
		}
		omitted = append(omitted, file)
	}
	return omitted
}

func buildCoverage(diff DiffSet, chunks []ReviewChunk, planned []plannedFile, opts ChunkOptions, requiredChunks int) CoverageSummary {
	coverage := CoverageSummary{
		Source:          diff.Source,
		TotalFiles:      len(diff.Files),
		ReviewMode:      CoverageModeFull,
		ChunksPlanned:   len(chunks),
		ChunksReviewed:  len(chunks),
		OmittedByReason: make(map[string]int),
		Warnings:        append([]string{}, diff.Warnings...),
		Complete:        true,
		RequiredChunks:  requiredChunks,
		MaxChunks:       opts.MaxChunks,
	}
	if len(chunks) > 1 {
		coverage.ReviewMode = CoverageModeChunked
	}
	if requiredChunks > opts.MaxChunks {
		coverage.ExceededMaxChunks = true
	}

	for _, pf := range planned {
		fc := FileCoverage{
			File:       pf.file,
			Path:       fileTitle(pf.file),
			Status:     string(statusOrUnknown(pf.file.Status)),
			Reviewed:   pf.reviewed,
			Truncated:  pf.truncated,
			Reason:     pf.reason,
			ChunkIndex: pf.chunkIndex,
		}
		if pf.reviewed {
			coverage.FilesReviewed++
			coverage.ReviewedFiles = append(coverage.ReviewedFiles, fc)
			if pf.truncated {
				coverage.FilesReviewedWithTruncatedDiffs++
				coverage.TruncatedFiles = append(coverage.TruncatedFiles, fc)
			}
			continue
		}
		fc.NotReviewed = true
		coverage.NotReviewedFiles = append(coverage.NotReviewedFiles, fc)
		coverage.OmittedByReason[valueOrUnknown(pf.reason)]++
	}

	coverage.FilesNotReviewed = 0
	coverage.FilesSummarizedNotReviewed = 0
	for _, file := range coverage.NotReviewedFiles {
		if IsAllowableNotReviewedFile(file) {
			coverage.FilesSummarizedNotReviewed++
			continue
		}
		coverage.FilesNotReviewed++
	}
	if coverage.ExceededMaxChunks || coverage.FilesNotReviewed > 0 {
		coverage.Complete = false
		coverage.ReviewMode = CoverageModePartial
		coverage.PartialReason = partialReason(coverage)
	}
	if len(coverage.NotReviewedFiles) > 0 {
		coverage.Warnings = append(coverage.Warnings, fmt.Sprintf("%d file(s) summarized or not reviewed at PR level", len(coverage.NotReviewedFiles)))
	}
	return coverage
}

// IsAllowableNotReviewedFile reports whether an unreviewed file is explicitly non-material.
func IsAllowableNotReviewedFile(file FileCoverage) bool {
	switch {
	case file.File.Generated:
		return true
	case file.File.Binary:
		return true
	case isLargeLockfileChange(file.File):
		return true
	case isModeOnly(file.File):
		return true
	default:
		return isSummarizedNotReviewedReason(file.Reason)
	}
}

func isSummarizedNotReviewedReason(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "generated file", "binary file", "large lockfile diff", "mode-only change", "metadata-only change", "file diff unavailable":
		return true
	default:
		return false
	}
}

func partialReason(coverage CoverageSummary) string {
	if coverage.ExceededMaxChunks {
		return "maximum planned review chunks exceeded"
	}
	if coverage.FilesNotReviewed > 0 {
		return "not all files were reviewed"
	}
	return "some files were deliberately summarized and not reviewed"
}

func renderChunk(diff DiffSet, chunk ReviewChunk, opts ChunkOptions) string {
	var body strings.Builder
	for _, file := range chunk.IncludedFiles {
		patch := file.Patch
		truncated := file.Truncated
		if truncated {
			patch = truncateBytes(file.Patch, opts.MaxFileDiffBytes)
		}
		writeFilePatch(&body, file, patch, truncated)
	}
	return renderChunkHeader(diff, chunk) + body.String() + renderOmittedSummary(chunk.OmittedFiles, opts.MaxOmittedSummaryEntries)
}

func renderChunkHeader(diff DiffSet, chunk ReviewChunk) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Review diff\n\n")
	fmt.Fprintf(&b, "- Source: %s\n", valueOrUnknown(diff.Source))
	if diff.PRNumber != 0 {
		fmt.Fprintf(&b, "- PR: #%d\n", diff.PRNumber)
	}
	fmt.Fprintf(&b, "- Base SHA: %s\n", valueOrUnknown(diff.BaseSHA))
	fmt.Fprintf(&b, "- Head SHA: %s\n", valueOrUnknown(diff.HeadSHA))
	fmt.Fprintf(&b, "- Total files: %d\n", len(diff.Files))
	mode := CoverageModeFull
	if chunk.Total > 1 {
		mode = CoverageModeChunked
	}
	fmt.Fprintf(&b, "- Review mode: %s\n", mode)
	fmt.Fprintf(&b, "- Chunk: %d of %d\n", chunk.Index, chunk.Total)
	if len(chunk.IncludedFiles) > 0 {
		first := fileTitle(chunk.IncludedFiles[0])
		last := fileTitle(chunk.IncludedFiles[len(chunk.IncludedFiles)-1])
		fmt.Fprintf(&b, "- Included path range: %s through %s\n", first, last)
	}
	for _, warning := range chunk.Warnings {
		fmt.Fprintf(&b, "- Warning: %s\n", warning)
	}
	b.WriteString("\n")
	return b.String()
}
