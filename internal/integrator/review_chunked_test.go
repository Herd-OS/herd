package integrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/herd-os/herd/internal/agent"
	agentprompt "github.com/herd-os/herd/internal/agent/prompt"
	"github.com/herd-os/herd/internal/config"
	"github.com/herd-os/herd/internal/issues"
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
				{Severity: "high", Description: "In this chunk, the visible diff still has a missing nil check."},
				{Severity: "LOW", Description: "Small cleanup"},
			},
		},
	}}

	aggregate, err := runChunkedReviewWithRetry(context.Background(), ag, &mockPlatform{prs: &mockPRService{}}, plan, agent.ReviewOptions{RepoRoot: "/repo"}, 50)

	require.NoError(t, err)
	require.NotNil(t, aggregate)
	result := aggregate.Result
	chunksReviewed := aggregate.ChunksReviewed
	require.NotNil(t, result)
	assert.Equal(t, 2, aggregate.ChunksReviewed)
	assert.Equal(t, 2, chunksReviewed)
	require.Len(t, aggregate.ChunkStats, 2)
	assert.Equal(t, chunkReviewStats{
		ChunkIndex:        1,
		TotalChunks:       2,
		HighFindingCount:  1,
		TotalFindingCount: 2,
		Findings: []agent.ReviewFinding{
			{Severity: "HIGH", Description: "Missing nil check"},
			{Severity: "CRITERIA", Description: "Acceptance criterion not verified"},
		},
	}, aggregate.ChunkStats[0])
	assert.Equal(t, chunkReviewStats{
		ChunkIndex:        2,
		TotalChunks:       2,
		HighFindingCount:  1,
		TotalFindingCount: 2,
		Findings: []agent.ReviewFinding{
			{Severity: "high", Description: "In this chunk, the visible diff still has a missing nil check."},
			{Severity: "LOW", Description: "Small cleanup"},
		},
	}, aggregate.ChunkStats[1])
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
	assert.Equal(t, "high", result.Findings[0].Severity)
	assert.Equal(t, "In this chunk, the visible diff still has a missing nil check.", result.Findings[0].Description)
	assert.Equal(t, "CRITERIA", result.Findings[1].Severity)
	assert.Equal(t, "LOW", result.Findings[2].Severity)
	assert.Equal(t, []string{
		"In this chunk, the visible diff still has a missing nil check.",
		"Acceptance criterion not verified",
		"Small cleanup",
	}, result.Comments)
	assert.Equal(t, reviewFindingDedupeStats{
		RawFindings:         4,
		FindingsAfterDedupe: 3,
		DedupedFindings:     1,
	}, aggregate.DedupeStats)
	assert.Contains(t, result.Summary, "Chunked review completed across 2 chunk(s)")
}

func TestReview_ChunkedPRLevelFindingDedupesBeforeCommentAndFixDispatch(t *testing.T) {
	results := []*agent.ReviewResult{
		{Approved: false, Summary: "chunk 1", Findings: []agent.ReviewFinding{
			{Severity: "HIGH", Description: "Repository owner feedback says Herd cascade-failed because of an unresolved merge conflict in this chunk."},
		}},
		{Approved: false, Summary: "chunk 2", Findings: []agent.ReviewFinding{
			{Severity: "high", Description: "Previous review says the herd cascade failed due to merge conflicts in chunk 2/3."},
		}},
		{Approved: false, Summary: "chunk 3", Findings: []agent.ReviewFinding{
			{Severity: "HIGH", Description: "Cycle 3 still reports a branch conflict in the conflict resolution cascade."},
		}},
	}
	for _, result := range results {
		result.Comments = reviewCommentsFromFindings(result.Findings)
	}

	issueSvc := newMockIssueService()
	issueSvc.listResult = []*platform.Issue{
		{Number: 42, Body: "---\nherd:\n  version: 1\n  batch: 1\n---\n\n## Task\nDo it\n"},
	}
	var createdBody string
	createdIssues := 0
	mockCreate := &mockIssueServiceWithCreate{
		mockIssueService: issueSvc,
		onCreate: func(title, body string, labels []string, milestone *int) (*platform.Issue, error) {
			createdIssues++
			createdBody = body
			return &platform.Issue{Number: 100, Title: title, Body: body}, nil
		},
	}
	dir, g, headSHA := initChunkedReviewRepo(t, 3)
	prSvc := newCapturingBatchPRService()
	prSvc.getResult[50].MergeableKnown = true
	prSvc.getResult[50].Mergeable = false
	prSvc.getResult[50].MergeStateStatus = "BLOCKED"
	prSvc.getResult[50].Labels = []string{issues.CascadeFailed}
	mock := newChunkedReviewPlatform(mockCreate, prSvc, headSHA)
	ag := &chunkCaptureAgent{results: results}

	result, err := Review(context.Background(), mock, ag, g, &config.Config{
		Integrator: config.Integrator{
			Review:             true,
			ReviewMaxFixCycles: 10,
			ReviewDiff:         config.ReviewDiff{MaxFilesPerChunk: 1},
		},
		Workers: config.Workers{TimeoutMinutes: 30},
	}, ReviewParams{PRNumber: 50, RepoRoot: dir})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Approved)
	assert.Equal(t, []int{100}, result.FixIssues)
	assert.Equal(t, 1, createdIssues)
	require.Len(t, mock.workflows.dispatched, 1)
	assert.Equal(t, "100", mock.workflows.dispatched[0]["issue_number"])
	assert.Empty(t, issueSvc.removedLabels[50])

	comment := requireCommentContaining(t, prSvc.comments, "## Review Aggregation")
	assert.Contains(t, comment, "- Raw findings before dedupe: 3")
	assert.Contains(t, comment, "- Findings after dedupe: 1")
	assert.Equal(t, 1, strings.Count(comment, "- Repository owner feedback says Herd cascade-failed"))
	assert.NotContains(t, comment, "Previous review says the herd cascade failed")
	assert.NotContains(t, comment, "Cycle 3 still reports")
	assert.Contains(t, createdBody, "Repository owner feedback says Herd cascade-failed")
	assert.NotContains(t, createdBody, "Previous review says the herd cascade failed")
	assert.NotContains(t, createdBody, "Cycle 3 still reports")
	assert.Contains(t, reviewEvents(prSvc.reviews), platform.ReviewRequestChanges)
}

func TestRunChunkedReviewWithRetryDedupesFileLevelDuplicateVariations(t *testing.T) {
	plan := reviewdiff.ChunkPlan{
		Chunks: []reviewdiff.ReviewChunk{
			{Index: 1, Total: 2, Text: "diff-a", IncludedFiles: []reviewdiff.ChangedFile{{Path: "cmd/herd-service/main.go"}}},
			{Index: 2, Total: 2, Text: "diff-b", IncludedFiles: []reviewdiff.ChangedFile{{Path: "cmd/herd-service/main.go"}}},
		},
		Coverage: reviewdiff.CoverageSummary{
			Source:         "test",
			TotalFiles:     1,
			ReviewMode:     reviewdiff.CoverageModeChunked,
			ChunksPlanned:  2,
			ChunksReviewed: 2,
			FilesReviewed:  1,
			Complete:       true,
		},
	}
	ag := &chunkCaptureAgent{results: []*agent.ReviewResult{
		{Approved: false, Summary: "chunk 1", Findings: []agent.ReviewFinding{
			{Severity: "HIGH", Description: "cmd/herd-service/main.go: production dependency wiring is wrong."},
		}},
		{Approved: false, Summary: "chunk 2", Findings: []agent.ReviewFinding{
			{Severity: "high", Description: "Visible diff: cmd/herd-service/main.go production dependency wiring wrong in this chunk."},
		}},
	}}

	aggregate, err := runChunkedReviewWithRetry(context.Background(), ag, &mockPlatform{prs: &mockPRService{}}, plan, agent.ReviewOptions{}, 50)

	require.NoError(t, err)
	require.NotNil(t, aggregate)
	require.NotNil(t, aggregate.Result)
	require.Len(t, aggregate.Result.Findings, 1)
	assert.Equal(t, "Visible diff: cmd/herd-service/main.go production dependency wiring wrong in this chunk.", aggregate.Result.Findings[0].Description)
	assert.Equal(t, reviewFindingDedupeStats{RawFindings: 2, FindingsAfterDedupe: 1, DedupedFindings: 1}, aggregate.DedupeStats)
}

func TestRunChunkedReviewWithRetryPreservesDistinctFindingsInSameFile(t *testing.T) {
	plan := reviewdiff.ChunkPlan{
		Chunks: []reviewdiff.ReviewChunk{
			{Index: 1, Total: 2, Text: "diff-a", IncludedFiles: []reviewdiff.ChangedFile{{Path: "cmd/herd-service/main.go"}}},
			{Index: 2, Total: 2, Text: "diff-b", IncludedFiles: []reviewdiff.ChangedFile{{Path: "cmd/herd-service/main.go"}}},
		},
		Coverage: reviewdiff.CoverageSummary{
			Source:         "test",
			TotalFiles:     1,
			ReviewMode:     reviewdiff.CoverageModeChunked,
			ChunksPlanned:  2,
			ChunksReviewed: 2,
			FilesReviewed:  1,
			Complete:       true,
		},
	}
	ag := &chunkCaptureAgent{results: []*agent.ReviewResult{
		{Approved: false, Summary: "chunk 1", Findings: []agent.ReviewFinding{
			{Severity: "HIGH", Description: "cmd/herd-service/main.go: function wireProductionDependencies drops the database provider."},
		}},
		{Approved: false, Summary: "chunk 2", Findings: []agent.ReviewFinding{
			{Severity: "HIGH", Description: "cmd/herd-service/main.go: function loadConfig ignores the HERD_CONFIG_PATH override."},
		}},
	}}

	aggregate, err := runChunkedReviewWithRetry(context.Background(), ag, &mockPlatform{prs: &mockPRService{}}, plan, agent.ReviewOptions{}, 50)

	require.NoError(t, err)
	require.NotNil(t, aggregate)
	require.NotNil(t, aggregate.Result)
	require.Len(t, aggregate.Result.Findings, 2)
	assert.Equal(t, reviewFindingDedupeStats{RawFindings: 2, FindingsAfterDedupe: 2}, aggregate.DedupeStats)
	assert.Equal(t, []string{
		"cmd/herd-service/main.go: function wireProductionDependencies drops the database provider.",
		"cmd/herd-service/main.go: function loadConfig ignores the HERD_CONFIG_PATH override.",
	}, []string{aggregate.Result.Findings[0].Description, aggregate.Result.Findings[1].Description})
}

func TestAppendReviewAggregationMetadata(t *testing.T) {
	tests := []struct {
		name        string
		dedupe      reviewFindingDedupeStats
		filter      reviewStateFilterStats
		want        []string
		doesNotWant []string
	}{
		{
			name:        "normal small review has no aggregation metadata",
			dedupe:      reviewFindingDedupeStats{RawFindings: 1, FindingsAfterDedupe: 1},
			doesNotWant: []string{"## Review Aggregation"},
		},
		{
			name:   "dedupe changed finding count",
			dedupe: reviewFindingDedupeStats{RawFindings: 3, FindingsAfterDedupe: 1, DedupedFindings: 2},
			want: []string{
				"## Review Aggregation",
				"- Raw findings before dedupe: 3",
				"- Findings after dedupe: 1",
			},
		},
		{
			name:   "stale filtered finding omits unchanged dedupe counts",
			dedupe: reviewFindingDedupeStats{RawFindings: 1, FindingsAfterDedupe: 1},
			filter: reviewStateFilterStats{StalePRStateFindingsIgnored: 1, CascadeLabelWasStale: true, CascadeLabelRemoved: true},
			want: []string{
				"- Stale PR-state findings ignored: 1",
				"- Stale cascade label cleanup: removed",
			},
			doesNotWant: []string{
				"- Raw findings before dedupe: 1",
				"- Findings after dedupe: 1",
			},
		},
		{
			name:   "stale filtered finding includes changed dedupe and stale counts",
			dedupe: reviewFindingDedupeStats{RawFindings: 3, FindingsAfterDedupe: 1, DedupedFindings: 2},
			filter: reviewStateFilterStats{StalePRStateFindingsIgnored: 1, CascadeLabelWasStale: true, CascadeLabelRemoved: true},
			want: []string{
				"- Raw findings before dedupe: 3",
				"- Findings after dedupe: 1",
				"- Stale PR-state findings ignored: 1",
				"- Stale cascade label cleanup: removed",
			},
		},
		{
			name:   "cleanup failure is concise",
			dedupe: reviewFindingDedupeStats{RawFindings: 1, FindingsAfterDedupe: 1},
			filter: reviewStateFilterStats{StalePRStateFindingsIgnored: 1, CascadeLabelWasStale: true, CascadeLabelRemoveError: "github failed"},
			want: []string{
				"- Stale cascade label cleanup: failed (github failed)",
			},
			doesNotWant: []string{"Stale finding was still ignored"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comment := appendReviewAggregationMetadata("body\n", tt.dedupe, tt.filter)
			for _, want := range tt.want {
				assert.Contains(t, comment, want)
			}
			for _, notWant := range tt.doesNotWant {
				assert.NotContains(t, comment, notWant)
			}
		})
	}
}

func TestRunChunkedReviewWithRetryRepeatedUnparseableReportsNoReviewedChunks(t *testing.T) {
	old := unparseableRetryDelay
	unparseableRetryDelay = 1 * time.Millisecond
	t.Cleanup(func() { unparseableRetryDelay = old })

	plan := reviewdiff.ChunkPlan{
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
		{IsUnparseable: true, Summary: "Failed to parse ..."},
		{IsUnparseable: true, Summary: "Failed to parse ..."},
	}}

	aggregate, err := runChunkedReviewWithRetry(context.Background(), ag, &mockPlatform{prs: &mockPRService{}}, plan, agent.ReviewOptions{}, 50)

	require.Error(t, err)
	assert.True(t, errors.Is(err, errManualInterventionNeeded))
	assert.Nil(t, aggregate)
	assert.Equal(t, []string{"diff-a", "diff-a"}, ag.diffs)
	require.Len(t, ag.opts, 2)
	assert.Equal(t, 1, ag.opts[0].ChunkIndex)
	assert.Equal(t, 1, ag.opts[1].ChunkIndex, "retry should reuse the first chunk instead of advancing")
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

	aggregate, err := runChunkedReviewWithRetry(context.Background(), ag, &mockPlatform{prs: &mockPRService{}}, plan, agent.ReviewOptions{}, 50)

	require.NoError(t, err)
	require.NotNil(t, aggregate)
	result := aggregate.Result
	chunksReviewed := aggregate.ChunksReviewed
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

	aggregate, err := runChunkedReviewWithRetry(context.Background(), ag, &mockPlatform{prs: &mockPRService{}}, plan, agent.ReviewOptions{}, 50)

	require.NoError(t, err)
	require.NotNil(t, aggregate)
	result := aggregate.Result
	chunksReviewed := aggregate.ChunksReviewed
	require.NotNil(t, result)
	assert.Equal(t, 1, chunksReviewed)
	require.Len(t, ag.opts, 1)
	require.Len(t, aggregate.ChunkStats, 1)
	assert.Equal(t, 2, aggregate.ChunkStats[0].TotalChunks)
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

func TestReviewSafetyValveHelpers(t *testing.T) {
	stats := []chunkReviewStats{
		{ChunkIndex: 1, TotalChunks: 3, HighFindingCount: safetyValveLimit},
		{
			ChunkIndex:        2,
			TotalChunks:       3,
			HighFindingCount:  safetyValveLimit + 1,
			TotalFindingCount: safetyValveLimit + 2,
			Findings: []agent.ReviewFinding{
				{Severity: "LOW", Description: "low issue"},
				{Severity: "high", Description: " first high "},
				{Severity: " HIGH ", Description: ""},
				{Severity: "HIGH", Description: "third high"},
				{Severity: "HIGH", Description: "fourth high"},
			},
		},
	}

	stat, ok := offendingChunkSafetyValveStats(stats, safetyValveLimit)

	require.True(t, ok)
	assert.Equal(t, 2, stat.ChunkIndex)

	chunkComment := buildChunkReviewSafetyValveComment(stat, safetyValveLimit)
	assert.Contains(t, chunkComment, "Agent review chunk 2/3 found 11 high-severity issues")
	assert.Contains(t, chunkComment, "- first high")
	assert.Contains(t, chunkComment, "- (no description)")
	assert.Contains(t, chunkComment, "- third high")
	assert.Contains(t, chunkComment, "- ...and 8 more high-severity finding(s) in this chunk.")
	assert.NotContains(t, chunkComment, "low issue")

	reviewComment := buildReviewSafetyValveComment(safetyValveLimit+1, safetyValveLimit)
	assert.Contains(t, reviewComment, "single review pass")
}

func TestReviewSafetyValveHelpersNoOffendingChunkAtLimit(t *testing.T) {
	stat, ok := offendingChunkSafetyValveStats([]chunkReviewStats{
		{ChunkIndex: 1, TotalChunks: 2, HighFindingCount: safetyValveLimit},
		{ChunkIndex: 2, TotalChunks: 2, HighFindingCount: safetyValveLimit - 1},
	}, safetyValveLimit)

	assert.False(t, ok)
	assert.Equal(t, chunkReviewStats{}, stat)
}

func TestMaterialNotReviewedClassifiesAllowableAndMaterialReasons(t *testing.T) {
	tests := []struct {
		name       string
		file       reviewdiff.FileCoverage
		reason     string
		wantCount  int
		wantBlocks bool
	}{
		{
			name: "generated",
			file: reviewdiff.FileCoverage{
				Path:        "dist/app.js",
				Reason:      "generated file",
				NotReviewed: true,
				File:        reviewdiff.ChangedFile{Path: "dist/app.js", Generated: true, Omitted: true, OmitReason: "generated file"},
			},
			wantCount: 0,
		},
		{
			name: "binary",
			file: reviewdiff.FileCoverage{
				Path:        "image.png",
				Reason:      "binary file",
				NotReviewed: true,
				File:        reviewdiff.ChangedFile{Path: "image.png", Binary: true, Omitted: true, OmitReason: "binary file"},
			},
			wantCount: 0,
		},
		{
			name: "large lockfile",
			file: reviewdiff.FileCoverage{
				Path:        "package-lock.json",
				Reason:      "large lockfile diff",
				NotReviewed: true,
				File:        reviewdiff.ChangedFile{Path: "package-lock.json", Patch: strings.Repeat("x", reviewdiff.LargeLockfileDiffBytes), Omitted: true, OmitReason: "large lockfile diff"},
			},
			wantCount: 0,
		},
		{
			name: "mode only",
			file: reviewdiff.FileCoverage{
				Path:        "script.sh",
				Reason:      "mode-only change",
				NotReviewed: true,
				File:        reviewdiff.ChangedFile{Path: "script.sh", PreviousMode: "100644", CurrentMode: "100755", Omitted: true, OmitReason: "mode-only change"},
			},
			wantCount: 0,
		},
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
			name:   "generated file diff unavailable",
			reason: "file diff unavailable",
			file: reviewdiff.FileCoverage{
				Path:        "dist/app.js",
				Reason:      "file diff unavailable",
				NotReviewed: true,
				File:        reviewdiff.ChangedFile{Path: "dist/app.js", Generated: true, Omitted: true, OmitReason: "file diff unavailable"},
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
			plan := reviewdiff.ChunkPlan{
				Chunks: []reviewdiff.ReviewChunk{{Index: 1, Total: 1}},
				Coverage: reviewdiff.CoverageSummary{
					NotReviewedFiles: []reviewdiff.FileCoverage{file},
				},
			}

			got := materialNotReviewed(plan)

			assert.Len(t, got, tt.wantCount)
			assert.Equal(t, tt.wantBlocks, coverageBlocksApproval(plan))
		})
	}
}

func TestRunChunkedReviewWithRetryApprovesMixedReviewWhenOnlyAllowableFilesAreOmitted(t *testing.T) {
	plan := reviewdiff.ChunkPlan{
		Chunks: []reviewdiff.ReviewChunk{
			{Index: 1, Total: 1, Text: "diff-a", IncludedFiles: []reviewdiff.ChangedFile{{Path: "src/app.go"}}, UsedDiffBytes: 10},
		},
		Coverage: reviewdiff.CoverageSummary{
			Source:        "test",
			TotalFiles:    2,
			ReviewMode:    reviewdiff.CoverageModeFull,
			ChunksPlanned: 1,
			FilesReviewed: 1,
			Complete:      true,
			NotReviewedFiles: []reviewdiff.FileCoverage{
				{
					Path:        "dist/app.js",
					Reason:      "generated file",
					NotReviewed: true,
					File:        reviewdiff.ChangedFile{Path: "dist/app.js", Generated: true, Omitted: true, OmitReason: "generated file"},
				},
			},
		},
	}
	ag := &chunkCaptureAgent{results: []*agent.ReviewResult{{Approved: true, Summary: "ok"}}}

	aggregate, err := runChunkedReviewWithRetry(context.Background(), ag, &mockPlatform{prs: &mockPRService{}}, plan, agent.ReviewOptions{}, 50)

	require.NoError(t, err)
	require.NotNil(t, aggregate)
	result := aggregate.Result
	chunksReviewed := aggregate.ChunksReviewed
	require.NotNil(t, result)
	assert.Equal(t, 1, chunksReviewed)
	assert.True(t, result.Approved)
	assert.Len(t, ag.opts, 1)
}

func TestRunChunkedReviewWithRetryDoesNotApproveZeroChunksWithOnlyAllowableOmissions(t *testing.T) {
	plan := reviewdiff.ChunkPlan{Coverage: reviewdiff.CoverageSummary{
		Source:        "test",
		TotalFiles:    1,
		ReviewMode:    reviewdiff.CoverageModeFull,
		ChunksPlanned: 0,
		Complete:      true,
		NotReviewedFiles: []reviewdiff.FileCoverage{
			{
				Path:        "dist/app.js",
				Reason:      "generated file",
				NotReviewed: true,
				File:        reviewdiff.ChangedFile{Path: "dist/app.js", Generated: true, Omitted: true, OmitReason: "generated file"},
			},
		},
	}}

	aggregate, err := runChunkedReviewWithRetry(context.Background(), &chunkCaptureAgent{}, &mockPlatform{prs: &mockPRService{}}, plan, agent.ReviewOptions{}, 50)

	require.NoError(t, err)
	require.NotNil(t, aggregate)
	result := aggregate.Result
	chunksReviewed := aggregate.ChunksReviewed
	require.NotNil(t, result)
	assert.Equal(t, 0, chunksReviewed)
	assert.False(t, result.Approved)
	assert.True(t, coverageBlocksApproval(plan))

	comment := buildCoverageApprovalBlockedComment(reviewdiff.PreparedDiff{}, plan)
	assert.Contains(t, comment, "No review chunks were sent to the review agent")
	assert.Contains(t, comment, "Files summarized but not reviewed")
	assert.Contains(t, comment, "dist/app.js: generated file")
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

	aggregate, err := runChunkedReviewWithRetry(context.Background(), &chunkCaptureAgent{}, &mockPlatform{prs: &mockPRService{}}, plan, agent.ReviewOptions{}, 50)

	require.NoError(t, err)
	require.NotNil(t, aggregate)
	result := aggregate.Result
	chunksReviewed := aggregate.ChunksReviewed
	require.NotNil(t, result)
	assert.Equal(t, 0, chunksReviewed)
	assert.False(t, result.Approved)

	comment := buildCoverageApprovalBlockedComment(reviewdiff.PreparedDiff{}, plan)
	assert.Contains(t, comment, "src/unavailable.go: patch unavailable from GitHub files API")
	assert.Contains(t, comment, "not all material source files were reviewed")
}

func TestRunChunkedReviewWithRetryBlocksGeneratedPathWithUnavailableDiff(t *testing.T) {
	plan := reviewdiff.ChunkPlan{
		Chunks: []reviewdiff.ReviewChunk{
			{Index: 1, Total: 1, Text: "diff-a", IncludedFiles: []reviewdiff.ChangedFile{{Path: "src/app.go"}}, UsedDiffBytes: 10},
		},
		Coverage: reviewdiff.CoverageSummary{
			Source:           "github-files-api",
			TotalFiles:       2,
			ReviewMode:       reviewdiff.CoverageModePartial,
			ChunksPlanned:    1,
			FilesReviewed:    1,
			FilesNotReviewed: 1,
			Complete:         false,
			NotReviewedFiles: []reviewdiff.FileCoverage{
				{
					Path:        "dist/app.js",
					Reason:      "file diff unavailable",
					NotReviewed: true,
					File: reviewdiff.ChangedFile{
						Path:       "dist/app.js",
						Status:     reviewdiff.ChangeModified,
						Generated:  true,
						Omitted:    true,
						OmitReason: "file diff unavailable",
					},
				},
			},
			OmittedByReason: map[string]int{"file diff unavailable": 1},
		},
	}
	ag := &chunkCaptureAgent{results: []*agent.ReviewResult{{Approved: true, Summary: "ok"}}}

	aggregate, err := runChunkedReviewWithRetry(context.Background(), ag, &mockPlatform{prs: &mockPRService{}}, plan, agent.ReviewOptions{}, 50)

	require.NoError(t, err)
	require.NotNil(t, aggregate)
	result := aggregate.Result
	chunksReviewed := aggregate.ChunksReviewed
	require.NotNil(t, result)
	assert.Equal(t, 1, chunksReviewed)
	assert.False(t, result.Approved)

	comment := buildCoverageApprovalBlockedComment(reviewdiff.PreparedDiff{}, plan)
	assert.Contains(t, comment, "dist/app.js: file diff unavailable")
	assert.Contains(t, comment, "not all material source files were reviewed")
}

var _ platform.Platform = (*mockPlatform)(nil)
