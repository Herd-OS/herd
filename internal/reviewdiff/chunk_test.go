package reviewdiff

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChunkForReviewSmallPRComplete(t *testing.T) {
	diff := DiffSet{
		PRNumber: 12,
		Source:   "github",
		BaseSHA:  "base",
		HeadSHA:  "head",
		Files: []ChangedFile{
			sourceFile("src/a.go", "aaa"),
			sourceFile("src/b.go", "bbb"),
		},
	}

	plan := ChunkForReview(diff, ChunkOptions{
		MaxChunkBytes:            100,
		MaxFileDiffBytes:         100,
		MaxFilesPerChunk:         10,
		MaxChunks:                8,
		MaxOmittedSummaryEntries: 10,
	})

	require.Len(t, plan.Chunks, 1)
	assert.Equal(t, []string{"src/a.go", "src/b.go"}, paths(plan.Chunks[0].IncludedFiles))
	assert.True(t, plan.Coverage.Complete)
	assert.Equal(t, CoverageModeFull, plan.Coverage.ReviewMode)
	assert.Equal(t, 2, plan.Coverage.FilesReviewed)
	assert.Zero(t, plan.Coverage.FilesNotReviewed)
	assert.Contains(t, plan.Chunks[0].Text, "# Review diff")
	assert.Contains(t, plan.Chunks[0].Text, "- Source: github")
	assert.Contains(t, plan.Chunks[0].Text, "- PR: #12")
	assert.Contains(t, plan.Chunks[0].Text, "- Base SHA: base")
	assert.Contains(t, plan.Chunks[0].Text, "- Head SHA: head")
	assert.Contains(t, plan.Chunks[0].Text, "- Total files: 2")
	assert.Contains(t, plan.Chunks[0].Text, "- Chunk: 1 of 1")
	assert.Contains(t, plan.Chunks[0].Text, "- Included path range: src/a.go through src/b.go")
	assert.NotContains(t, plan.Chunks[0].Text, "```markdown")
}

func TestChunkForReviewMultipleChunksIncludesEveryReviewableFileOnce(t *testing.T) {
	files := make([]ChangedFile, 0, 120)
	for i := range 120 {
		files = append(files, sourceFile(fmt.Sprintf("src/%03d.go", i), strings.Repeat("x", 8)))
	}

	plan := ChunkForReview(DiffSet{Source: "local-git", Files: files}, ChunkOptions{
		MaxChunkBytes:            1000,
		MaxFileDiffBytes:         100,
		MaxFilesPerChunk:         31,
		MaxChunks:                8,
		MaxOmittedSummaryEntries: 10,
	})

	require.Len(t, plan.Chunks, 4)
	assert.True(t, plan.Coverage.Complete)
	assert.Equal(t, CoverageModeChunked, plan.Coverage.ReviewMode)
	assert.Equal(t, 120, plan.Coverage.FilesReviewed)
	seen := map[string]int{}
	for _, chunk := range plan.Chunks {
		require.LessOrEqual(t, len(chunk.IncludedFiles), 31)
		assert.Contains(t, chunk.Text, "- Review mode: chunked")
		for _, file := range chunk.IncludedFiles {
			seen[file.Path]++
		}
	}
	for _, file := range files {
		assert.Equal(t, 1, seen[file.Path], file.Path)
	}
	assert.Equal(t, "src/000.go", plan.Chunks[0].IncludedFiles[0].Path)
	assert.Equal(t, "src/119.go", plan.Chunks[3].IncludedFiles[len(plan.Chunks[3].IncludedFiles)-1].Path)
}

func TestChunkForReviewRequiredChunksGroupsFilesAfterMaxChunks(t *testing.T) {
	files := make([]ChangedFile, 0, 8)
	for i := range 8 {
		files = append(files, sourceFile(fmt.Sprintf("src/%03d.go", i), strings.Repeat("x", 8)))
	}

	plan := ChunkForReview(DiffSet{Source: "github", Files: files}, ChunkOptions{
		MaxChunkBytes:            1000,
		MaxFileDiffBytes:         100,
		MaxFilesPerChunk:         3,
		MaxChunks:                1,
		MaxOmittedSummaryEntries: 10,
	})

	require.Len(t, plan.Chunks, 1)
	assert.Equal(t, 3, plan.Coverage.RequiredChunks)
	assert.True(t, plan.Coverage.ExceededMaxChunks)
	assert.Equal(t, 3, plan.Chunks[0].Total)
	assert.Equal(t, 3, plan.Coverage.FilesReviewed)
	assert.Equal(t, 5, plan.Coverage.FilesNotReviewed)
	assert.Equal(t, map[string]int{"max chunks reached": 5}, plan.Coverage.OmittedByReason)
	assert.Contains(t, plan.Chunks[0].Text, "- Review mode: chunked")
	assert.Contains(t, plan.Chunks[0].Text, "- Chunk: 1 of 3")
	assert.NotContains(t, plan.Chunks[0].Text, "- Review mode: full")
}

func TestBuildCoverageMaxChunksExceededIsPartialWithoutMaterialOmissions(t *testing.T) {
	diff := DiffSet{
		Source: "github",
		Files: []ChangedFile{
			sourceFile("src/001.go", "aaa"),
			sourceFile("src/002.go", "bbb"),
		},
	}
	chunks := []ReviewChunk{{
		Index:         1,
		Total:         2,
		IncludedFiles: []ChangedFile{diff.Files[0]},
		UsedDiffBytes: 3,
	}}
	planned := []plannedFile{
		{file: diff.Files[0], reviewable: true, reviewed: true, chunkIndex: 1},
		{file: diff.Files[1], reviewable: true, reviewed: true, chunkIndex: 2},
	}

	plan := ChunkPlan{
		DiffSet: diff,
		Chunks:  chunks,
		Coverage: buildCoverage(diff, chunks, planned, ChunkOptions{
			MaxChunkBytes:            100,
			MaxFileDiffBytes:         100,
			MaxFilesPerChunk:         1,
			MaxChunks:                1,
			MaxOmittedSummaryEntries: 10,
		}, 2),
	}
	summary := FormatChunkedCoverageSummary(plan, len(plan.Chunks), 10)

	assert.False(t, plan.Coverage.Complete)
	assert.Equal(t, CoverageModePartial, plan.Coverage.ReviewMode)
	assert.Equal(t, "maximum planned review chunks exceeded", plan.Coverage.PartialReason)
	assert.True(t, plan.Coverage.ExceededMaxChunks)
	assert.Zero(t, plan.Coverage.FilesNotReviewed)
	assert.Contains(t, summary, "- This is a partial review: maximum planned review chunks exceeded.")
	assert.Contains(t, summary, "- Required chunks: 2; max chunks: 1")
	assert.Contains(t, summary, "- Chunks reviewed: 1/2")
	assert.NotContains(t, summary, "Review coverage is complete")
}

func TestChunkForReviewSummarizesNonReviewableFilesWithPreciseReasons(t *testing.T) {
	largeLockPatch := strings.Repeat("x", LargeLockfileDiffBytes)
	diff := DiffSet{
		Source: "github-files-api",
		Files: []ChangedFile{
			{Path: "dist/app.js", Status: ChangeModified, Patch: "@@ -1 +1 @@\n-old\n+new\n"},
			{Path: "assets/image.png", Status: ChangeAdded, Binary: true},
			{Path: "package-lock.json", Status: ChangeModified, Patch: largeLockPatch},
			{Path: "script.sh", Status: ChangeModified, PreviousMode: "100644", CurrentMode: "100755"},
			{Path: "src/unavailable.go", Status: ChangeModified, Omitted: true},
			sourceFile("src/reviewed.go", "ok"),
		},
	}

	plan := ChunkForReview(diff, ChunkOptions{
		MaxChunkBytes:            LargeLockfileDiffBytes + 100,
		MaxFileDiffBytes:         LargeLockfileDiffBytes + 100,
		MaxFilesPerChunk:         10,
		MaxChunks:                8,
		MaxOmittedSummaryEntries: 10,
	})

	require.Len(t, plan.Chunks, 1)
	assert.Equal(t, []string{"src/reviewed.go"}, paths(plan.Chunks[0].IncludedFiles))
	assert.False(t, plan.Coverage.Complete)
	assert.Equal(t, CoverageModePartial, plan.Coverage.ReviewMode)
	assert.Equal(t, 4, plan.Coverage.FilesSummarizedNotReviewed)
	assert.Equal(t, 1, plan.Coverage.FilesNotReviewed)
	assert.Equal(t, map[string]int{
		"binary file":                   1,
		"generated file":                1,
		"large lockfile diff":           1,
		"mode-only change":              1,
		"patch unavailable from source": 1,
	}, plan.Coverage.OmittedByReason)
	summary := FormatChunkedCoverageSummary(plan, len(plan.Chunks), 10)
	assert.Contains(t, summary, "partial review")
	assert.Contains(t, summary, "- Files not reviewed: 1")
	assert.Contains(t, summary, "dist/app.js")
	assert.Contains(t, summary, "assets/image.png")
	assert.Contains(t, summary, "package-lock.json")
	assert.Contains(t, summary, "script.sh")
	assert.Contains(t, summary, "src/unavailable.go")
}

func TestChunkForReviewOmittedGeneratedPathPreservesSourceUnavailableReason(t *testing.T) {
	tests := []struct {
		name         string
		file         ChangedFile
		wantComplete bool
	}{
		{
			name: "default source unavailable reason",
			file: ChangedFile{
				Path:    "dist/app.js",
				Status:  ChangeModified,
				Omitted: true,
			},
			wantComplete: false,
		},
		{
			name: "custom blocking source unavailable reason",
			file: ChangedFile{
				Path:       "src/material.js",
				Status:     ChangeModified,
				Omitted:    true,
				OmitReason: "source unavailable: material file",
			},
			wantComplete: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := ChunkForReview(DiffSet{Source: "github", Files: []ChangedFile{tt.file}}, ChunkOptions{
				MaxChunkBytes:            100,
				MaxFileDiffBytes:         100,
				MaxFilesPerChunk:         10,
				MaxChunks:                8,
				MaxOmittedSummaryEntries: 10,
			})

			assert.Empty(t, plan.Chunks)
			assert.Equal(t, tt.wantComplete, plan.Coverage.Complete)
			require.Len(t, plan.Coverage.NotReviewedFiles, 1)
			assert.Equal(t, firstReason(tt.file.OmitReason, "patch unavailable from source"), plan.Coverage.NotReviewedFiles[0].Reason)
			assert.NotEqual(t, "generated file", plan.Coverage.NotReviewedFiles[0].Reason)
			if tt.wantComplete {
				assert.Equal(t, 1, plan.Coverage.FilesSummarizedNotReviewed)
				assert.Zero(t, plan.Coverage.FilesNotReviewed)
			} else {
				assert.Zero(t, plan.Coverage.FilesSummarizedNotReviewed)
				assert.Equal(t, 1, plan.Coverage.FilesNotReviewed)
			}
			assert.Equal(t, 1, plan.Coverage.OmittedByReason[plan.Coverage.NotReviewedFiles[0].Reason])
		})
	}
}

func TestChunkForReviewSourceUnavailableMaterialFileBlocksCoverage(t *testing.T) {
	plan := ChunkForReview(DiffSet{Source: "github-files-api", Files: []ChangedFile{
		{Path: "src/unavailable.go", Status: ChangeModified, Omitted: true, OmitReason: "patch unavailable from GitHub files API"},
	}}, ChunkOptions{
		MaxChunkBytes:            100,
		MaxFileDiffBytes:         100,
		MaxFilesPerChunk:         10,
		MaxChunks:                8,
		MaxOmittedSummaryEntries: 10,
	})

	assert.Empty(t, plan.Chunks)
	assert.False(t, plan.Coverage.Complete)
	assert.Equal(t, CoverageModePartial, plan.Coverage.ReviewMode)
	assert.Zero(t, plan.Coverage.FilesSummarizedNotReviewed)
	assert.Equal(t, 1, plan.Coverage.FilesNotReviewed)
	require.Len(t, plan.Coverage.NotReviewedFiles, 1)
	assert.Equal(t, "src/unavailable.go", plan.Coverage.NotReviewedFiles[0].Path)
	assert.Equal(t, "patch unavailable from GitHub files API", plan.Coverage.NotReviewedFiles[0].Reason)

	summary := FormatChunkedCoverageSummary(plan, len(plan.Chunks), 10)
	assert.Contains(t, summary, "- This is a partial review: not all files were reviewed.")
	assert.Contains(t, summary, "- Files not reviewed: 1")
	assert.Contains(t, summary, "patch unavailable from GitHub files API: 1")
	assert.Contains(t, summary, "src/unavailable.go")
}

func TestChunkForReviewOmittedMaterialFileWithSummarizedReasonBlocksCoverage(t *testing.T) {
	plan := ChunkForReview(DiffSet{Source: "github-files-api", Files: []ChangedFile{
		{Path: "src/material.go", Status: ChangeModified, Omitted: true, OmitReason: "file diff unavailable"},
	}}, ChunkOptions{
		MaxChunkBytes:            100,
		MaxFileDiffBytes:         100,
		MaxFilesPerChunk:         10,
		MaxChunks:                8,
		MaxOmittedSummaryEntries: 10,
	})

	assert.Empty(t, plan.Chunks)
	assert.False(t, plan.Coverage.Complete)
	assert.Equal(t, CoverageModePartial, plan.Coverage.ReviewMode)
	assert.Zero(t, plan.Coverage.FilesSummarizedNotReviewed)
	assert.Equal(t, 1, plan.Coverage.FilesNotReviewed)
	require.Len(t, plan.Coverage.NotReviewedFiles, 1)
	assert.Equal(t, "src/material.go", plan.Coverage.NotReviewedFiles[0].Path)
	assert.Equal(t, "file diff unavailable", plan.Coverage.NotReviewedFiles[0].Reason)
	assert.False(t, IsAllowableNotReviewedFile(plan.Coverage.NotReviewedFiles[0]))

	summary := FormatChunkedCoverageSummary(plan, len(plan.Chunks), 10)
	assert.Contains(t, summary, "- This is a partial review: not all files were reviewed.")
	assert.Contains(t, summary, "- Files not reviewed: 1")
	assert.Contains(t, summary, "file diff unavailable: 1")
	assert.Contains(t, summary, "src/material.go")
}

func TestChunkForReviewOversizedSingleFile(t *testing.T) {
	tests := []struct {
		name              string
		patch             string
		opts              ChunkOptions
		wantReviewed      bool
		wantTruncated     bool
		wantNotReviewed   int
		wantTruncatedDiff int
		wantReason        string
	}{
		{
			name:  "truncated file fits chunk",
			patch: strings.Repeat("+x\n", 20),
			opts: ChunkOptions{
				MaxChunkBytes:            80,
				MaxFileDiffBytes:         40,
				MaxFilesPerChunk:         10,
				MaxChunks:                8,
				MaxOmittedSummaryEntries: 10,
			},
			wantReviewed:      true,
			wantTruncated:     true,
			wantTruncatedDiff: 1,
			wantReason:        "file diff exceeds per-file limit and was truncated",
		},
		{
			name:  "truncated file cannot fit chunk",
			patch: strings.Repeat("+x\n", 20),
			opts: ChunkOptions{
				MaxChunkBytes:            20,
				MaxFileDiffBytes:         40,
				MaxFilesPerChunk:         10,
				MaxChunks:                8,
				MaxOmittedSummaryEntries: 10,
			},
			wantNotReviewed: 1,
			wantReason:      "file diff exceeds maximum reviewable size",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := ChunkForReview(DiffSet{Source: "github", Files: []ChangedFile{
				sourceFile("src/large.go", tt.patch),
			}}, tt.opts)

			assert.Equal(t, boolToInt(tt.wantReviewed), plan.Coverage.FilesReviewed)
			assert.Equal(t, tt.wantTruncatedDiff, plan.Coverage.FilesReviewedWithTruncatedDiffs)
			assert.Equal(t, tt.wantNotReviewed, plan.Coverage.FilesNotReviewed)
			if tt.wantReviewed {
				require.Len(t, plan.Chunks, 1)
				assert.Equal(t, tt.wantTruncated, plan.Chunks[0].IncludedFiles[0].Truncated)
				assert.Equal(t, tt.wantReason, plan.Coverage.ReviewedFiles[0].Reason)
				assert.Contains(t, plan.Chunks[0].Text, "[diff truncated]")
			} else {
				assert.Empty(t, plan.Chunks)
				require.Len(t, plan.Coverage.NotReviewedFiles, 1)
				assert.Equal(t, tt.wantReason, plan.Coverage.NotReviewedFiles[0].Reason)
			}
		})
	}
}

func TestFormatChunkedCoverageSummaryCompleteAndPartialWording(t *testing.T) {
	complete := ChunkForReview(DiffSet{
		Source: "github",
		Files:  []ChangedFile{sourceFile("src/a.go", "aaa")},
	}, ChunkOptions{MaxChunkBytes: 100, MaxFileDiffBytes: 100, MaxFilesPerChunk: 10, MaxChunks: 8})
	completeSummary := FormatChunkedCoverageSummary(complete, len(complete.Chunks), 10)
	assert.Contains(t, completeSummary, "Review coverage is complete")
	assert.NotContains(t, completeSummary, "partial review")

	files := []ChangedFile{
		sourceFile("src/001.go", strings.Repeat("a", 20)),
		sourceFile("src/002.go", strings.Repeat("b", 20)),
		sourceFile("src/003.go", strings.Repeat("c", 20)),
	}
	partial := ChunkForReview(DiffSet{Source: "github", Files: files}, ChunkOptions{
		MaxChunkBytes:            30,
		MaxFileDiffBytes:         100,
		MaxFilesPerChunk:         1,
		MaxChunks:                2,
		MaxOmittedSummaryEntries: 10,
	})
	partialSummary := FormatChunkedCoverageSummary(partial, len(partial.Chunks), 10)

	assert.False(t, partial.Coverage.Complete)
	assert.True(t, partial.Coverage.ExceededMaxChunks)
	assert.Contains(t, partialSummary, "partial review")
	assert.Contains(t, partialSummary, "Chunk 1/3: 1 files")
	assert.Contains(t, partialSummary, "Chunk 2/3: 1 files")
	assert.Contains(t, partialSummary, "max chunks reached: 1")
	assert.Contains(t, partialSummary, "src/003.go")
	assert.NotContains(t, partialSummary, "total diff byte limit reached")
}

func TestFormatChunkedCoverageSummaryCapsStablePathExamples(t *testing.T) {
	plan := ChunkForReview(DiffSet{
		Source: "github",
		Files: []ChangedFile{
			{Path: "src/001.go", Status: ChangeModified, Omitted: true},
			{Path: "src/002.go", Status: ChangeModified, Omitted: true},
			{Path: "src/003.go", Status: ChangeModified, Omitted: true},
		},
	}, ChunkOptions{MaxChunkBytes: 100, MaxFileDiffBytes: 100, MaxFilesPerChunk: 10, MaxChunks: 8})

	summary := FormatChunkedCoverageSummary(plan, len(plan.Chunks), 2)

	first := strings.Index(summary, "src/001.go")
	second := strings.Index(summary, "src/002.go")
	third := strings.Index(summary, "src/003.go")
	require.NotEqual(t, -1, first)
	require.NotEqual(t, -1, second)
	assert.Less(t, first, second)
	assert.Equal(t, -1, third)
	assert.Contains(t, summary, "... 1 additional files not shown")
}

func TestDefaultChunkOptions(t *testing.T) {
	opts := DefaultChunkOptions()
	assert.Equal(t, DefaultMaxTotalDiffBytes, opts.MaxChunkBytes)
	assert.Equal(t, DefaultMaxFileDiffBytes, opts.MaxFileDiffBytes)
	assert.Equal(t, DefaultMaxIncludedFiles, opts.MaxFilesPerChunk)
	assert.Equal(t, 8, opts.MaxChunks)
	assert.Equal(t, DefaultMaxOmittedSummaryEntries, opts.MaxOmittedSummaryEntries)
	assert.Equal(t, opts, normalizeChunkOptions(ChunkOptions{}))
}

func sourceFile(path, patch string) ChangedFile {
	return ChangedFile{
		Path:      path,
		Status:    ChangeModified,
		Additions: 1,
		Deletions: 1,
		Changes:   2,
		Patch:     patch,
	}
}

func paths(files []ChangedFile) []string {
	got := make([]string, 0, len(files))
	for _, file := range files {
		got = append(got, file.Path)
	}
	return got
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
