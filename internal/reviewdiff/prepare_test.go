package reviewdiff

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/git"
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

func TestPrepareForReviewFallsBackWhenSuppliedHeadSHAMissingLocally(t *testing.T) {
	diffMarker := filepath.Join(t.TempDir(), "diff-called")
	withFakeGit(t, `#!/bin/sh
if [ "$1" = "rev-parse" ]; then
  case "$2" in
    base^{commit}) echo base; exit 0 ;;
  esac
  exit 1
fi
if [ "$1" = "fetch" ]; then
  exit 1
fi
if [ "$1" = "diff" ]; then
  touch "$DIFF_MARKER"
  echo diff should not run >&2
  exit 1
fi
exit 1
`)
	t.Setenv("DIFF_MARKER", diffMarker)
	repoRoot := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repoRoot, ".git"), 0755))
	prs := &mockPreparePRService{diff: "diff --git a/fallback.go b/fallback.go\n+fallback\n"}

	prepared, err := PrepareForReview(context.Background(), PrepareRequest{
		PRNumber:     42,
		BaseSHA:      "base",
		HeadSHA:      "missing-head",
		RepoRoot:     repoRoot,
		Git:          git.New(repoRoot),
		PullRequests: prs,
	})

	require.NoError(t, err)
	assert.True(t, prs.getDiffCalled)
	assert.False(t, prs.listFilesCalled)
	assert.Equal(t, GitHubRawDiffSource, prepared.DiffSet.Source)
	assert.Contains(t, prepared.Rendered.Text, "fallback.go")
	assert.NoFileExists(t, diffMarker)
	assert.Contains(t, strings.Join(prepared.Rendered.Warnings, "\n"), "local git diff collection failed")
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

func TestPrepareForReviewParsesRawDiffIntoFilesBeforeRendering(t *testing.T) {
	raw := strings.Join([]string{
		"diff --git a/first.go b/first.go\n",
		"index 1111111..2222222 100644\n",
		"--- a/first.go\n",
		"+++ b/first.go\n",
		"@@ -1 +1 @@\n",
		"-old\n",
		"+new\n",
		"diff --git a/second.go b/second.go\n",
		"index 3333333..4444444 100644\n",
		"--- a/second.go\n",
		"+++ b/second.go\n",
		"@@ -1,20000 +1,20000 @@\n",
		strings.Repeat("+x\n", (DefaultMaxFileDiffBytes/3)+1),
		"diff --git a/third.go b/third.go\n",
		"index 5555555..6666666 100644\n",
		"--- a/third.go\n",
		"+++ b/third.go\n",
		"@@ -1 +1 @@\n",
		"-before\n",
		"+after\n",
	}, "")
	require.Greater(t, len(raw), DefaultMaxFileDiffBytes)
	require.Less(t, len(raw), DefaultMaxTotalDiffBytes)

	prs := &mockPreparePRService{diff: raw}

	prepared, err := PrepareForReview(context.Background(), PrepareRequest{
		PRNumber:     7,
		PullRequests: prs,
	})

	require.NoError(t, err)
	require.Len(t, prepared.DiffSet.Files, 3)
	assert.Equal(t, []string{"first.go", "second.go", "third.go"}, []string{
		prepared.DiffSet.Files[0].Path,
		prepared.DiffSet.Files[1].Path,
		prepared.DiffSet.Files[2].Path,
	})
	assert.Contains(t, prepared.Rendered.Text, "## first.go")
	assert.Contains(t, prepared.Rendered.Text, "## second.go")
	assert.Contains(t, prepared.Rendered.Text, "## third.go")
	assert.NotContains(t, prepared.Rendered.Text, "pull-request.diff")
	require.Len(t, prepared.Rendered.TruncatedFiles, 1)
	assert.Equal(t, "second.go", prepared.Rendered.TruncatedFiles[0].Path)

	summary := FormatCoverageSummary(prepared.DiffSet, prepared.Rendered, 10)
	assert.Contains(t, summary, "Truncated files: 1")
	assert.Contains(t, summary, "Included files: 3")
	assert.Contains(t, summary, "second.go: per-file diff byte limit reached")
}

func TestPrepareForReviewParsesQuotedRawDiffPaths(t *testing.T) {
	tests := []struct {
		name    string
		oldPath string
		newPath string
		status  ChangeStatus
	}{
		{
			name:    "spaces tabs quotes and backslashes",
			oldPath: "dir/old path\twith \"quote\" and \\ slash.go",
			newPath: "dir/new path\twith \"quote\" and \\ slash.go",
			status:  ChangeModified,
		},
		{
			name:    "new file quoted header",
			oldPath: "",
			newPath: "added/new file\twith \"quote\" and \\ slash.go",
			status:  ChangeAdded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldHeaderPath := "/dev/null"
			oldDiffGitPath := "/dev/null"
			if tt.oldPath != "" {
				oldHeaderPath = strconv.Quote("a/" + tt.oldPath)
				oldDiffGitPath = oldHeaderPath
			}
			raw := strings.Join([]string{
				"diff --git " + oldDiffGitPath + " " + strconv.Quote("b/"+tt.newPath) + "\n",
				"new file mode 100644\n",
				"index 0000000..1111111 100644\n",
				"--- " + oldHeaderPath + "\t2026-07-11 00:00:00 +0000\n",
				"+++ " + strconv.Quote("b/"+tt.newPath) + "\t2026-07-11 00:00:00 +0000\n",
				"@@ -0,0 +1 @@\n",
				"+new\n",
			}, "")
			if tt.oldPath != "" {
				raw = strings.Replace(raw, "new file mode 100644\n", "", 1)
				raw = strings.Replace(raw, "0000000..1111111", "1111111..2222222", 1)
			}

			prepared, err := PrepareForReview(context.Background(), PrepareRequest{
				PRNumber:     7,
				PullRequests: &mockPreparePRService{diff: raw},
			})

			require.NoError(t, err)
			require.Len(t, prepared.DiffSet.Files, 1)
			file := prepared.DiffSet.Files[0]
			assert.Equal(t, tt.newPath, file.Path)
			assert.Equal(t, tt.oldPath, file.OldPath)
			assert.Equal(t, tt.status, file.Status)
			assert.Contains(t, prepared.Rendered.Text, "## "+tt.newPath)
			assert.NotContains(t, prepared.Rendered.Text, "pull-request.diff")
		})
	}
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
	assert.Contains(t, summary, "big.go: per-file diff byte limit reached")
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
