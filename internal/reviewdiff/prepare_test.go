package reviewdiff

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareForReviewFallsBackToFilesWhenRawDiffTooLarge(t *testing.T) {
	prs := &mockPreparePRService{
		diffErr: platform.ErrPullRequestDiffTooLarge,
		files: []*platform.PullRequestFile{
			{
				Path:      "src/app.go",
				Status:    "modified",
				Additions: 1,
				Deletions: 1,
				Changes:   2,
				Patch:     "@@ -1 +1 @@\n-old\n+new\n",
			},
		},
	}

	prepared, err := PrepareForReview(context.Background(), PrepareRequest{
		PRNumber:     42,
		BaseSHA:      "base",
		HeadSHA:      "head",
		PullRequests: prs,
	})

	require.NoError(t, err)
	assert.True(t, prs.getDiffCalled)
	assert.True(t, prs.listFilesCalled)
	assert.Equal(t, "github-files-api", prepared.DiffSet.Source)
	assert.Contains(t, prepared.Rendered.Text, "src/app.go")
	assert.Contains(t, prepared.Rendered.Text, "raw GitHub pull request diff was too large")
}

func TestPrepareForReviewPrefersLocalGitCollection(t *testing.T) {
	dir, g := initLocalCollectorRepo(t)
	require.NoError(t, g.CreateBranch("feature", "main"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "local.txt"), []byte("local\n"), 0644))
	runLocalGit(t, dir, "add", "local.txt")
	runLocalGit(t, dir, "commit", "-m", "local change")

	prs := &mockPreparePRService{
		diffErr: errors.New("raw diff should not be needed"),
	}

	prepared, err := PrepareForReview(context.Background(), PrepareRequest{
		PRNumber:     42,
		BaseRef:      "main",
		HeadRef:      "feature",
		RepoRoot:     dir,
		Git:          g,
		PullRequests: prs,
	})

	require.NoError(t, err)
	assert.False(t, prs.getDiffCalled)
	assert.False(t, prs.listFilesCalled)
	assert.Equal(t, LocalGitSource, prepared.DiffSet.Source)
	assert.Contains(t, prepared.Rendered.Text, "local.txt")
	assert.Contains(t, prepared.Rendered.Text, "+local")
}

func TestPrepareForReviewUsesRawDiffWhenAvailable(t *testing.T) {
	prs := &mockPreparePRService{diff: "diff --git a/a.go b/a.go\n+raw\n"}

	prepared, err := PrepareForReview(context.Background(), PrepareRequest{
		PRNumber:     7,
		PullRequests: prs,
	})

	require.NoError(t, err)
	assert.True(t, prs.getDiffCalled)
	assert.False(t, prs.listFilesCalled)
	assert.Equal(t, GitHubRawDiffSource, prepared.DiffSet.Source)
	assert.Contains(t, prepared.Rendered.Text, "+raw")
}

func TestFormatCoverageSummaryReportsLimitedFiles(t *testing.T) {
	result := RenderForReview(DiffSet{
		Source: "test-source",
		Files: []ChangedFile{
			{Path: "included.go", Status: ChangeModified, Patch: "@@ -1 +1 @@\n-a\n+b\n"},
			{Path: "generated.pb.go", Status: ChangeModified, Generated: true, Patch: "@@ -1 +1 @@\n-a\n+b\n"},
			{Path: "image.png", Status: ChangeAdded, Binary: true},
			{Path: "big.go", Status: ChangeModified, Patch: strings.Repeat("+x\n", 100)},
		},
	}, RenderOptions{
		MaxTotalDiffBytes:        1000,
		MaxFileDiffBytes:         20,
		MaxIncludedFiles:         10,
		MaxOmittedSummaryEntries: 10,
	})
	require.True(t, result.WasLimited)

	summary := FormatCoverageSummary(DiffSet{Source: "test-source"}, result, 10)

	assert.Contains(t, summary, "## Diff Coverage")
	assert.Contains(t, summary, "Source: test-source")
	assert.Contains(t, summary, "Included files: 2")
	assert.Contains(t, summary, "Omitted files: 2 (generated: 1, binary: 1)")
	assert.Contains(t, summary, "Truncated files: 1")
	assert.Contains(t, summary, "generated.pb.go: generated file")
	assert.Contains(t, summary, "image.png: binary file")
}

type mockPreparePRService struct {
	diff            string
	diffErr         error
	files           []*platform.PullRequestFile
	filesErr        error
	getDiffCalled   bool
	listFilesCalled bool
}

func (m *mockPreparePRService) Create(context.Context, string, string, string, string) (*platform.PullRequest, error) {
	return nil, nil
}
func (m *mockPreparePRService) Get(context.Context, int) (*platform.PullRequest, error) {
	return nil, nil
}
func (m *mockPreparePRService) List(context.Context, platform.PRFilters) ([]*platform.PullRequest, error) {
	return nil, nil
}
func (m *mockPreparePRService) Update(context.Context, int, *string, *string) (*platform.PullRequest, error) {
	return nil, nil
}
func (m *mockPreparePRService) Merge(context.Context, int, platform.MergeMethod) (*platform.MergeResult, error) {
	return nil, nil
}
func (m *mockPreparePRService) UpdateBranch(context.Context, int) error { return nil }
func (m *mockPreparePRService) CreateReview(context.Context, int, string, platform.ReviewEvent) error {
	return nil
}
func (m *mockPreparePRService) AddComment(context.Context, int, string) error { return nil }
func (m *mockPreparePRService) ListReviewComments(context.Context, int) ([]*platform.ReviewComment, error) {
	return nil, nil
}
func (m *mockPreparePRService) ListFiles(context.Context, int) ([]*platform.PullRequestFile, error) {
	m.listFilesCalled = true
	if m.filesErr != nil {
		return nil, m.filesErr
	}
	return m.files, nil
}
func (m *mockPreparePRService) GetDiff(context.Context, int) (string, error) {
	m.getDiffCalled = true
	if m.diffErr != nil {
		return "", m.diffErr
	}
	return m.diff, nil
}
func (m *mockPreparePRService) Close(context.Context, int) error { return nil }
