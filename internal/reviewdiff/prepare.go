package reviewdiff

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/herd-os/herd/internal/git"
	"github.com/herd-os/herd/internal/platform"
)

const GitHubRawDiffSource = "github-raw-diff"

type PrepareRequest struct {
	PRNumber     int
	BaseRef      string
	HeadRef      string
	BaseSHA      string
	HeadSHA      string
	RepoRoot     string
	Git          *git.Git
	PullRequests platform.PullRequestService
}

type PreparedDiff struct {
	DiffSet  DiffSet
	Rendered RenderResult
}

func PrepareForReview(ctx context.Context, req PrepareRequest) (PreparedDiff, error) {
	var warnings []string
	if localAvailable(req) {
		diff, err := (LocalCollector{Git: req.Git}).Collect(ctx, CollectRequest{
			PRNumber: req.PRNumber,
			BaseRef:  req.BaseRef,
			HeadRef:  req.HeadRef,
			BaseSHA:  req.BaseSHA,
			HeadSHA:  req.HeadSHA,
		})
		if err == nil {
			return renderPrepared(diff), nil
		}
		warning := fmt.Sprintf("local git diff collection failed; falling back to GitHub: %v", err)
		fmt.Printf("Warning: %s\n", warning)
		warnings = append(warnings, warning)
	}

	if req.PullRequests == nil {
		return PreparedDiff{}, fmt.Errorf("preparing review diff: pull request service is required")
	}

	raw, rawErr := req.PullRequests.GetDiff(ctx, req.PRNumber)
	if rawErr == nil {
		diff := rawDiffSet(req, raw)
		diff.Warnings = append(diff.Warnings, warnings...)
		return renderPrepared(diff), nil
	}

	if !platform.IsPullRequestDiffTooLarge(rawErr) {
		fmt.Printf("Warning: raw PR diff unavailable; falling back to GitHub files API: %v\n", rawErr)
	} else {
		fmt.Printf("Warning: raw PR diff too large; falling back to GitHub files API.\n")
	}

	files, filesErr := req.PullRequests.ListFiles(ctx, req.PRNumber)
	if filesErr != nil {
		return PreparedDiff{}, fmt.Errorf("preparing review diff: raw diff failed: %w; files fallback failed: %w", rawErr, filesErr)
	}

	diff := FromPlatformFiles(req.PRNumber, req.BaseSHA, req.HeadSHA, files)
	diff.Warnings = append(diff.Warnings, warnings...)
	if platform.IsPullRequestDiffTooLarge(rawErr) {
		diff.Warnings = append(diff.Warnings, "raw GitHub pull request diff was too large; using GitHub files API metadata and bounded patches")
	} else {
		diff.Warnings = append(diff.Warnings, fmt.Sprintf("raw GitHub pull request diff unavailable; using GitHub files API metadata and bounded patches: %v", rawErr))
	}
	return renderPrepared(diff), nil
}

func FormatCoverageSummary(diff DiffSet, rendered RenderResult, maxOmittedEntries int) string {
	if maxOmittedEntries <= 0 {
		maxOmittedEntries = DefaultMaxOmittedSummaryEntries
	}

	generated, binary := 0, 0
	for _, file := range rendered.OmittedFiles {
		if file.Generated {
			generated++
		}
		if file.Binary {
			binary++
		}
	}

	var b strings.Builder
	b.WriteString("## Diff Coverage\n\n")
	fmt.Fprintf(&b, "- Source: %s\n", valueOrUnknown(diff.Source))
	fmt.Fprintf(&b, "- Included files: %d\n", len(rendered.IncludedFiles))
	fmt.Fprintf(&b, "- Omitted files: %d (generated: %d, binary: %d)\n", len(rendered.OmittedFiles), generated, binary)
	fmt.Fprintf(&b, "- Truncated files: %d\n", len(rendered.TruncatedFiles))
	if len(rendered.Warnings) > 0 {
		b.WriteString("- Warnings:\n")
		for _, warning := range rendered.Warnings {
			fmt.Fprintf(&b, "  - %s\n", warning)
		}
	}
	if len(rendered.OmittedFiles) > 0 {
		b.WriteString("- Omitted paths:\n")
		limit := min(maxOmittedEntries, len(rendered.OmittedFiles))
		for _, file := range rendered.OmittedFiles[:limit] {
			fmt.Fprintf(&b, "  - %s: %s\n", fileTitle(file), valueOrUnknown(file.OmitReason))
		}
		if len(rendered.OmittedFiles) > limit {
			fmt.Fprintf(&b, "  - ... %d additional omitted files not shown\n", len(rendered.OmittedFiles)-limit)
		}
	}
	return b.String()
}

func renderPrepared(diff DiffSet) PreparedDiff {
	return PreparedDiff{
		DiffSet:  diff,
		Rendered: RenderForReview(diff, DefaultRenderOptions()),
	}
}

func rawDiffSet(req PrepareRequest, raw string) DiffSet {
	return DiffSet{
		PRNumber: req.PRNumber,
		BaseSHA:  req.BaseSHA,
		HeadSHA:  req.HeadSHA,
		Source:   GitHubRawDiffSource,
		Files: []ChangedFile{{
			Path:   "pull-request.diff",
			Status: ChangeModified,
			Patch:  raw,
		}},
	}
}

func localAvailable(req PrepareRequest) bool {
	if req.Git == nil || req.RepoRoot == "" {
		return false
	}
	if req.Git.WorkDir == "" {
		return false
	}
	if (req.BaseSHA == "" && req.BaseRef == "") || (req.HeadSHA == "" && req.HeadRef == "") {
		return false
	}
	if !sameCleanPath(req.Git.WorkDir, req.RepoRoot) {
		return false
	}
	if _, err := os.Stat(filepath.Join(req.RepoRoot, ".git")); err != nil {
		return false
	}
	return true
}

func sameCleanPath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA == nil {
		a = absA
	}
	if errB == nil {
		b = absB
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
