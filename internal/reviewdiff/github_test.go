package reviewdiff

import (
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFromPlatformFilesMapsMetadataAndPatches(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		wantStatus ChangeStatus
	}{
		{name: "added", status: "added", wantStatus: ChangeAdded},
		{name: "modified", status: "modified", wantStatus: ChangeModified},
		{name: "removed", status: "removed", wantStatus: ChangeDeleted},
		{name: "renamed", status: "renamed", wantStatus: ChangeRenamed},
		{name: "changed", status: "changed", wantStatus: ChangeModified},
		{name: "unknown", status: "weird", wantStatus: ChangeUnknown},
	}

	files := make([]*platform.PullRequestFile, 0, len(tests)+1)
	for _, tt := range tests {
		files = append(files, &platform.PullRequestFile{
			Path:         tt.name + ".go",
			PreviousPath: "old/" + tt.name + ".go",
			Status:       tt.status,
			Additions:    2,
			Deletions:    1,
			Changes:      3,
			Patch:        "@@ -1 +1 @@\n-old\n+new\n",
		})
	}
	files = append(files, nil)

	diff := FromPlatformFiles(42, "base-sha", "head-sha", files)
	require.Len(t, diff.Files, len(tests))
	assert.Equal(t, 42, diff.PRNumber)
	assert.Equal(t, "base-sha", diff.BaseSHA)
	assert.Equal(t, "head-sha", diff.HeadSHA)
	assert.Equal(t, "github-files-api", diff.Source)

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := diff.Files[i]
			assert.Equal(t, tt.name+".go", file.Path)
			assert.Equal(t, "old/"+tt.name+".go", file.OldPath)
			assert.Equal(t, tt.wantStatus, file.Status)
			assert.Equal(t, 2, file.Additions)
			assert.Equal(t, 1, file.Deletions)
			assert.Equal(t, 3, file.Changes)
			assert.Equal(t, "@@ -1 +1 @@\n-old\n+new\n", file.Patch)
			assert.False(t, file.Omitted)
		})
	}
}

func TestFromPlatformFilesMarksUnavailableMetadataGeneratedAndLarge(t *testing.T) {
	largePatch := strings.Repeat("+lock\n", LargeLockfileDiffBytes/5+1)
	diff := FromPlatformFiles(7, "base", "head", []*platform.PullRequestFile{
		{
			Path:         "new/name.go",
			PreviousPath: "old/name.go",
			Status:       "renamed",
			Additions:    0,
			Deletions:    0,
			Changes:      0,
		},
		{
			Path:      "src/huge.go",
			Status:    "modified",
			Additions: 1000,
			Deletions: 1000,
			Changes:   2000,
		},
		{
			Path:   "dist/app.js",
			Status: "modified",
			Patch:  "@@ -1 +1 @@\n-old\n+new\n",
		},
		{
			Path:   "package-lock.json",
			Status: "modified",
			Patch:  largePatch,
		},
	})

	require.Len(t, diff.Files, 4)
	assert.False(t, diff.Files[0].Binary)
	assert.True(t, diff.Files[0].Omitted)
	assert.Equal(t, ChangeRenamed, diff.Files[0].Status)
	assert.Equal(t, "old/name.go", diff.Files[0].OldPath)
	assert.Equal(t, "metadata-only change", diff.Files[0].OmitReason)
	assert.True(t, diff.Files[1].Omitted)
	assert.Equal(t, "patch unavailable from GitHub files API", diff.Files[1].OmitReason)
	assert.True(t, diff.Files[2].Generated)
	assert.True(t, diff.Files[3].Large)

	result := RenderForReview(diff, RenderOptions{
		MaxTotalDiffBytes:        LargeLockfileDiffBytes + 1000,
		MaxFileDiffBytes:         LargeLockfileDiffBytes + 1000,
		MaxIncludedFiles:         10,
		MaxOmittedSummaryEntries: 10,
	})
	require.True(t, result.WasLimited)
	assert.Contains(t, result.Text, "new/name.go (renamed, old path: old/name.go, +0/-0, reason: metadata-only change)")
	assert.Contains(t, result.Text, "src/huge.go (modified, +1000/-1000, reason: patch unavailable from GitHub files API)")
	assert.Contains(t, result.Text, "dist/app.js (modified, +0/-0, reason: generated file)")
	assert.Contains(t, result.Text, "package-lock.json (modified, +0/-0, reason: large lockfile diff)")
}

func TestFromPlatformFilesMarksPatchlessLikelyBinaryPaths(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "image", path: "assets/screenshot.png"},
		{name: "archive", path: "release/app.tar.gz"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diff := FromPlatformFiles(42, "base", "head", []*platform.PullRequestFile{
				{
					Path:   tt.path,
					Status: "added",
				},
			})
			require.Len(t, diff.Files, 1)
			assert.True(t, diff.Files[0].Binary)
			assert.True(t, diff.Files[0].Omitted)
			assert.Equal(t, "binary file", diff.Files[0].OmitReason)

			result := RenderForReview(diff, DefaultRenderOptions())
			require.True(t, result.WasLimited)
			require.Len(t, result.OmittedFiles, 1)
			assert.True(t, result.OmittedFiles[0].Binary)
			assert.Contains(t, result.Text, tt.path+" (added, +0/-0, reason: binary file)")

			summary := FormatCoverageSummary(diff, result, 10)
			assert.Contains(t, summary, "Omitted files: 1 (generated: 0, binary: 1)")
			assert.Contains(t, summary, tt.path+": binary file")
		})
	}
}
