package reviewdiff

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

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
	Chunks   []ReviewChunk
	Coverage CoverageSummary
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
	if len(rendered.TruncatedFiles) > 0 {
		b.WriteString("- Truncated paths:\n")
		limit := min(maxOmittedEntries, len(rendered.TruncatedFiles))
		for _, file := range rendered.TruncatedFiles[:limit] {
			fmt.Fprintf(&b, "  - %s: %s\n", fileTitle(file), valueOrUnknown(file.OmitReason))
		}
		if len(rendered.TruncatedFiles) > limit {
			fmt.Fprintf(&b, "  - ... %d additional truncated files not shown\n", len(rendered.TruncatedFiles)-limit)
		}
	}
	return b.String()
}

func renderPrepared(diff DiffSet) PreparedDiff {
	plan := ChunkForReview(diff, DefaultChunkOptions())
	return PreparedDiff{
		DiffSet:  diff,
		Rendered: RenderForReview(diff, DefaultRenderOptions()),
		Chunks:   plan.Chunks,
		Coverage: plan.Coverage,
	}
}

func rawDiffSet(req PrepareRequest, raw string) DiffSet {
	files := parseRawDiffFiles(raw)
	if len(files) == 0 {
		files = []ChangedFile{{
			Path:   "pull-request.diff",
			Status: ChangeModified,
			Patch:  raw,
		}}
	}
	return DiffSet{
		PRNumber: req.PRNumber,
		BaseSHA:  req.BaseSHA,
		HeadSHA:  req.HeadSHA,
		Source:   GitHubRawDiffSource,
		Files:    files,
	}
}

func parseRawDiffFiles(raw string) []ChangedFile {
	var files []ChangedFile
	var current *ChangedFile
	var patch strings.Builder

	flush := func() {
		if current == nil {
			return
		}
		current.Patch = patch.String()
		current.Changes = current.Additions + current.Deletions
		if current.Path == "" {
			current.Path = current.OldPath
		}
		files = append(files, MarkGeneratedAndLarge(*current, DefaultRenderOptions()))
		current = nil
		patch.Reset()
	}

	for _, line := range strings.SplitAfter(raw, "\n") {
		lineNoNewline := strings.TrimSuffix(line, "\n")
		if strings.HasPrefix(line, "diff --git ") {
			flush()
			oldPath, path := parseDiffGitPaths(lineNoNewline)
			current = &ChangedFile{
				Path:    path,
				OldPath: oldPath,
				Status:  ChangeModified,
			}
		}
		if current == nil {
			continue
		}
		patch.WriteString(line)
		applyRawDiffLine(current, lineNoNewline)
	}
	flush()

	return files
}

func parseDiffGitPaths(line string) (string, string) {
	rest := strings.TrimSpace(strings.TrimPrefix(line, "diff --git "))
	first, rest, ok := nextDiffPathToken(rest)
	if !ok {
		return "", ""
	}
	second, _, ok := nextDiffPathToken(rest)
	if !ok {
		return "", ""
	}
	return trimDiffPathPrefix(first), trimDiffPathPrefix(second)
}

func nextDiffPathToken(s string) (string, string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", false
	}
	if s[0] != '"' {
		i := strings.IndexFunc(s, unicode.IsSpace)
		if i < 0 {
			return s, "", true
		}
		return s[:i], s[i:], true
	}

	escaped := false
	for i := 1; i < len(s); i++ {
		switch {
		case escaped:
			escaped = false
		case s[i] == '\\':
			escaped = true
		case s[i] == '"':
			token, err := strconv.Unquote(s[:i+1])
			if err != nil {
				return s[1:i], s[i+1:], true
			}
			return token, s[i+1:], true
		}
	}
	return strings.Trim(s, `"`), "", true
}

func trimDiffPathPrefix(path string) string {
	switch {
	case path == "/dev/null":
		return ""
	case strings.HasPrefix(path, "a/"), strings.HasPrefix(path, "b/"):
		return path[2:]
	default:
		return path
	}
}

func applyRawDiffLine(file *ChangedFile, line string) {
	switch {
	case strings.HasPrefix(line, "new file mode "):
		file.Status = ChangeAdded
		file.CurrentMode = strings.TrimPrefix(line, "new file mode ")
	case strings.HasPrefix(line, "deleted file mode "):
		file.Status = ChangeDeleted
		file.PreviousMode = strings.TrimPrefix(line, "deleted file mode ")
	case strings.HasPrefix(line, "old mode "):
		file.PreviousMode = strings.TrimPrefix(line, "old mode ")
	case strings.HasPrefix(line, "new mode "):
		file.CurrentMode = strings.TrimPrefix(line, "new mode ")
	case strings.HasPrefix(line, "rename from "):
		file.Status = ChangeRenamed
		file.OldPath = strings.TrimPrefix(line, "rename from ")
	case strings.HasPrefix(line, "rename to "):
		file.Status = ChangeRenamed
		file.Path = strings.TrimPrefix(line, "rename to ")
	case strings.HasPrefix(line, "--- "):
		if path := diffHeaderPath(line, "--- "); path != "" {
			file.OldPath = path
		}
	case strings.HasPrefix(line, "+++ "):
		if path := diffHeaderPath(line, "+++ "); path != "" {
			file.Path = path
		}
	case strings.HasPrefix(line, "Binary files "), strings.HasPrefix(line, "GIT binary patch"):
		file.Binary = true
	case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
		file.Additions++
	case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
		file.Deletions++
	}
}

func diffHeaderPath(line, prefix string) string {
	path, _, ok := nextDiffPathToken(strings.TrimPrefix(line, prefix))
	if !ok {
		return ""
	}
	return trimDiffPathPrefix(path)
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
