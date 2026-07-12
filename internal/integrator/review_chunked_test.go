package integrator

import (
	"context"
	"testing"

	"github.com/herd-os/herd/internal/agent"
	agentprompt "github.com/herd-os/herd/internal/agent/prompt"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/platform"
	"github.com/herd-os/herd/internal/reviewdiff"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type chunkCaptureAgent struct {
	results []*agent.ReviewResult
	diffs   []string
	opts    []agent.ReviewOptions
}

func (a *chunkCaptureAgent) Plan(_ context.Context, _ string, _ agent.PlanOptions) (*agent.Plan, error) {
	return nil, nil
}

func (a *chunkCaptureAgent) Execute(_ context.Context, _ agent.TaskSpec, _ agent.ExecOptions) (*agent.ExecResult, error) {
	return nil, nil
}

func (a *chunkCaptureAgent) Review(_ context.Context, diff string, opts agent.ReviewOptions) (*agent.ReviewResult, error) {
	a.diffs = append(a.diffs, diff)
	a.opts = append(a.opts, opts)
	idx := len(a.opts) - 1
	if idx >= len(a.results) {
		idx = len(a.results) - 1
	}
	return a.results[idx], nil
}

func (a *chunkCaptureAgent) Discuss(_ context.Context, _ agent.DiscussOptions) error {
	return nil
}

func TestChunkOptionsFromConfig(t *testing.T) {
	cfg := &config.Config{Integrator: config.Integrator{ReviewDiff: config.ReviewDiff{
		MaxChunkBytes:    123,
		MaxFileBytes:     45,
		MaxFilesPerChunk: 6,
		MaxChunks:        7,
	}}}

	got := chunkOptionsFromConfig(cfg)

	assert.Equal(t, 123, got.MaxChunkBytes)
	assert.Equal(t, 45, got.MaxFileDiffBytes)
	assert.Equal(t, 6, got.MaxFilesPerChunk)
	assert.Equal(t, 7, got.MaxChunks)
	assert.Equal(t, reviewdiff.DefaultMaxOmittedSummaryEntries, got.MaxOmittedSummaryEntries)
}

func TestRunChunkedReviewWithRetryAggregatesMetadataAndDedupesAcrossChunks(t *testing.T) {
	plan := reviewdiff.ChunkPlan{
		DiffSet: reviewdiff.DiffSet{Source: "test", Files: []reviewdiff.ChangedFile{
			{Path: "a.go"},
			{Path: "b.go"},
		}},
		Chunks: []reviewdiff.ReviewChunk{
			{Index: 1, Total: 2, Text: "diff-a", IncludedFiles: []reviewdiff.ChangedFile{{Path: "a.go"}}, UsedDiffBytes: 10},
			{Index: 2, Total: 2, Text: "diff-b", IncludedFiles: []reviewdiff.ChangedFile{{Path: "b.go"}}, UsedDiffBytes: 10},
		},
		Coverage: reviewdiff.CoverageSummary{
			Source:         "test",
			TotalFiles:     2,
			ReviewMode:     reviewdiff.CoverageModeChunked,
			ChunksPlanned:  2,
			ChunksReviewed: 2,
			FilesReviewed:  2,
			Complete:       true,
		},
	}
	ag := &chunkCaptureAgent{results: []*agent.ReviewResult{
		{
			Approved: true,
			Summary:  "first ok",
			Findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Missing nil check"},
				{Severity: "CRITERIA", Description: "Acceptance criterion not verified"},
			},
		},
		{
			Approved: false,
			Summary:  "second found issue",
			Findings: []agent.ReviewFinding{
				{Severity: "high", Description: "  Missing   nil check  "},
				{Severity: "LOW", Description: "Small cleanup"},
			},
		},
	}}

	result, chunksReviewed, err := runChunkedReviewWithRetry(context.Background(), ag, &mockPlatform{prs: &mockPRService{}}, plan, agent.ReviewOptions{RepoRoot: "/repo"}, 50)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 2, chunksReviewed)
	assert.False(t, result.Approved)
	assert.Equal(t, []string{"diff-a", "diff-b"}, ag.diffs)
	require.Len(t, ag.opts, 2)
	assert.Equal(t, 1, ag.opts[0].ChunkIndex)
	assert.Equal(t, 2, ag.opts[0].TotalChunks)
	assert.Equal(t, "a.go", ag.opts[0].ChunkIncludedPathRange)
	assert.True(t, ag.opts[0].ChunkedReview)
	assert.False(t, ag.opts[0].PartialReview)
	assert.Contains(t, ag.opts[0].CoverageSummary, "Chunks reviewed: 2/2")
	assert.Equal(t, 2, ag.opts[1].ChunkIndex)
	assert.Equal(t, "b.go", ag.opts[1].ChunkIncludedPathRange)
	require.Len(t, result.Findings, 3)
	assert.Equal(t, "HIGH", result.Findings[0].Severity)
	assert.Equal(t, "CRITERIA", result.Findings[1].Severity)
	assert.Equal(t, "LOW", result.Findings[2].Severity)
	assert.Contains(t, result.Summary, "Chunked review completed across 2 chunk(s)")
}

func TestRunChunkedReviewWithRetryMaxChunksDoesNotApprove(t *testing.T) {
	chunks := make([]reviewdiff.ReviewChunk, 8)
	results := make([]*agent.ReviewResult, 8)
	for i := range chunks {
		chunks[i] = reviewdiff.ReviewChunk{Index: i + 1, Total: 8, Text: "diff", IncludedFiles: []reviewdiff.ChangedFile{{Path: "file.go"}}}
		results[i] = &agent.ReviewResult{Approved: true, Summary: "ok"}
	}
	plan := reviewdiff.ChunkPlan{
		Chunks: chunks,
		Coverage: reviewdiff.CoverageSummary{
			Source:            "test",
			TotalFiles:        20,
			ReviewMode:        reviewdiff.CoverageModePartial,
			ChunksPlanned:     8,
			FilesReviewed:     8,
			FilesNotReviewed:  12,
			Complete:          false,
			ExceededMaxChunks: true,
			RequiredChunks:    20,
			MaxChunks:         8,
		},
	}
	ag := &chunkCaptureAgent{results: results}

	result, chunksReviewed, err := runChunkedReviewWithRetry(context.Background(), ag, &mockPlatform{prs: &mockPRService{}}, plan, agent.ReviewOptions{}, 50)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 8, chunksReviewed)
	assert.Len(t, ag.opts, 8)
	assert.False(t, result.Approved)
}

func TestRunChunkedReviewWithRetryMaxChunksOneIncludesRequiredChunkScope(t *testing.T) {
	plan := reviewdiff.ChunkForReview(reviewdiff.DiffSet{
		Source: "test",
		Files: []reviewdiff.ChangedFile{
			{Path: "a.go", Status: reviewdiff.ChangeModified, Patch: "@@ -1 +1 @@\n-old\n+new\n"},
			{Path: "b.go", Status: reviewdiff.ChangeModified, Patch: "@@ -1 +1 @@\n-old\n+new\n"},
		},
	}, reviewdiff.ChunkOptions{
		MaxChunkBytes:            1000,
		MaxFileDiffBytes:         1000,
		MaxFilesPerChunk:         1,
		MaxChunks:                1,
		MaxOmittedSummaryEntries: reviewdiff.DefaultMaxOmittedSummaryEntries,
	})
	require.Len(t, plan.Chunks, 1)
	require.True(t, plan.Coverage.ExceededMaxChunks)
	require.Equal(t, 2, plan.Coverage.RequiredChunks)
	ag := &chunkCaptureAgent{results: []*agent.ReviewResult{{Approved: true, Summary: "ok"}}}

	result, chunksReviewed, err := runChunkedReviewWithRetry(context.Background(), ag, &mockPlatform{prs: &mockPRService{}}, plan, agent.ReviewOptions{}, 50)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, chunksReviewed)
	require.Len(t, ag.opts, 1)
	assert.True(t, ag.opts[0].ChunkedReview)
	assert.Equal(t, 1, ag.opts[0].ChunkIndex)
	assert.Equal(t, 2, ag.opts[0].TotalChunks)
	assert.Contains(t, ag.opts[0].CoverageSummary, "Chunks reviewed: 1/2")
	assert.Contains(t, ag.opts[0].CoverageSummary, "Required chunks: 2; max chunks: 1")
	assert.Contains(t, result.Summary, "Chunk 1/2: ok")

	prompt, err := agentprompt.RenderReviewPrompt(ag.diffs[0], ag.opts[0])
	require.NoError(t, err)
	assert.Contains(t, prompt, "## Review Chunk")
	assert.Contains(t, prompt, "Chunk: 1 of 2")
	assert.Contains(t, ag.diffs[0], "- Chunk: 1 of 2")
	assert.Contains(t, ag.diffs[0], "- Review mode: chunked")
	assert.NotContains(t, ag.diffs[0], "- Review mode: full")
	assert.Contains(t, prompt, "Review only the included diffs in this chunk")
}

func TestMaterialNotReviewedClassifiesAllowableAndMaterialReasons(t *testing.T) {
	tests := []struct {
		name       string
		file       reviewdiff.FileCoverage
		reason     string
		wantCount  int
		wantBlocks bool
	}{
		{name: "generated", reason: "generated file", wantCount: 0},
		{name: "binary", reason: "binary file", wantCount: 0},
		{name: "large lockfile", reason: "large lockfile diff", wantCount: 0},
		{name: "mode only", reason: "mode-only change", wantCount: 0},
		{
			name:   "generated source unavailable",
			reason: "patch unavailable from source",
			file: reviewdiff.FileCoverage{
				Path:        "dist/app.js",
				Reason:      "patch unavailable from source",
				NotReviewed: true,
				File:        reviewdiff.ChangedFile{Path: "dist/app.js", Generated: true, Omitted: true},
			},
			wantCount:  1,
			wantBlocks: true,
		},
		{
			name:   "generated file reason",
			reason: "generated file",
			file: reviewdiff.FileCoverage{
				Path:        "dist/app.js",
				Reason:      "generated file",
				NotReviewed: true,
				File:        reviewdiff.ChangedFile{Path: "dist/app.js", Generated: true, Omitted: true, OmitReason: "generated file"},
			},
			wantCount: 0,
		},
		{name: "source unavailable", reason: "patch unavailable from source", wantCount: 1, wantBlocks: true},
		{name: "github source unavailable", reason: "patch unavailable from GitHub files API", wantCount: 1, wantBlocks: true},
		{name: "max chunks", reason: "max chunks reached", wantCount: 1, wantBlocks: true},
		{name: "maximum reviewable size", reason: "file diff exceeds maximum reviewable size", wantCount: 1, wantBlocks: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := tt.file
			if file.Path == "" {
				file = reviewdiff.FileCoverage{Path: "file.go", Reason: tt.reason, NotReviewed: true}
			}
			plan := reviewdiff.ChunkPlan{Coverage: reviewdiff.CoverageSummary{
				NotReviewedFiles: []reviewdiff.FileCoverage{file},
			}}

			got := materialNotReviewed(plan)

			assert.Len(t, got, tt.wantCount)
			assert.Equal(t, tt.wantBlocks, coverageBlocksApproval(plan))
		})
	}
}

func TestRunChunkedReviewWithRetryApprovesWhenOnlyAllowableFilesAreOmitted(t *testing.T) {
	plan := reviewdiff.ChunkPlan{Coverage: reviewdiff.CoverageSummary{
		Source:        "test",
		TotalFiles:    1,
		ReviewMode:    reviewdiff.CoverageModeFull,
		ChunksPlanned: 0,
		Complete:      true,
		NotReviewedFiles: []reviewdiff.FileCoverage{
			{Path: "dist/app.js", Reason: "generated file", NotReviewed: true},
		},
	}}

	result, chunksReviewed, err := runChunkedReviewWithRetry(context.Background(), &chunkCaptureAgent{}, &mockPlatform{prs: &mockPRService{}}, plan, agent.ReviewOptions{}, 50)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, chunksReviewed)
	assert.True(t, result.Approved)
}

func TestRunChunkedReviewWithRetryBlocksSourceUnavailableMaterialFile(t *testing.T) {
	plan := reviewdiff.ChunkPlan{Coverage: reviewdiff.CoverageSummary{
		Source:           "github-files-api",
		TotalFiles:       1,
		ReviewMode:       reviewdiff.CoverageModePartial,
		FilesNotReviewed: 1,
		Complete:         false,
		NotReviewedFiles: []reviewdiff.FileCoverage{
			{
				Path:        "src/unavailable.go",
				Reason:      "patch unavailable from GitHub files API",
				NotReviewed: true,
				File: reviewdiff.ChangedFile{
					Path:       "src/unavailable.go",
					Status:     reviewdiff.ChangeModified,
					Omitted:    true,
					OmitReason: "patch unavailable from GitHub files API",
				},
			},
		},
		OmittedByReason: map[string]int{"patch unavailable from GitHub files API": 1},
	}}

	result, chunksReviewed, err := runChunkedReviewWithRetry(context.Background(), &chunkCaptureAgent{}, &mockPlatform{prs: &mockPRService{}}, plan, agent.ReviewOptions{}, 50)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 0, chunksReviewed)
	assert.False(t, result.Approved)

	comment := buildCoverageApprovalBlockedComment(reviewdiff.PreparedDiff{}, plan)
	assert.Contains(t, comment, "src/unavailable.go: patch unavailable from GitHub files API")
	assert.Contains(t, comment, "not all material source files were reviewed")
}

var _ platform.Platform = (*mockPlatform)(nil)
