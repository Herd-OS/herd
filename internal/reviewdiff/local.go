package reviewdiff

import (
	"context"
	"fmt"
	"strings"

	"github.com/herd-os/herd/internal/git"
)

const LocalGitSource = "local-git"

type LocalCollector struct {
	Git    *git.Git
	Remote string
}

type CollectRequest struct {
	PRNumber int
	BaseRef  string
	HeadRef  string
	BaseSHA  string
	HeadSHA  string
}

func (c LocalCollector) Collect(ctx context.Context, req CollectRequest) (DiffSet, error) {
	if err := ctx.Err(); err != nil {
		return DiffSet{}, err
	}
	if c.Git == nil {
		return DiffSet{}, fmt.Errorf("local diff collector requires git")
	}

	remote := c.Remote
	if remote == "" {
		remote = "origin"
	}

	base, err := c.resolveRef(req.BaseSHA, req.BaseRef, remote, 0)
	if err != nil {
		return DiffSet{}, fmt.Errorf("resolve base ref: %w", err)
	}
	head, err := c.resolveRef(req.HeadSHA, req.HeadRef, remote, req.PRNumber)
	if err != nil {
		return DiffSet{}, fmt.Errorf("resolve head ref: %w", err)
	}

	entries, err := c.Git.DiffNameStatus(base, head)
	if err != nil {
		return DiffSet{}, fmt.Errorf("compute changed-file metadata: %w", err)
	}
	numstat, err := c.Git.DiffNumStat(base, head)
	if err != nil {
		return DiffSet{}, fmt.Errorf("compute changed-file stats: %w", err)
	}

	diff := DiffSet{
		PRNumber: req.PRNumber,
		BaseSHA:  firstNonEmpty(req.BaseSHA, base),
		HeadSHA:  firstNonEmpty(req.HeadSHA, head),
		Source:   LocalGitSource,
		Files:    make([]ChangedFile, 0, len(entries)),
	}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return DiffSet{}, err
		}
		file := changedFileFromNameStatus(entry)
		stat, ok := lookupNumStat(numstat, file.Path, file.OldPath)
		if ok {
			if stat[0] < 0 || stat[1] < 0 {
				file.Binary = true
			} else {
				file.Additions = stat[0]
				file.Deletions = stat[1]
				file.Changes = stat[0] + stat[1]
			}
		}

		if !file.Binary {
			patch, patchErr := c.Git.DiffPath(base, head, diffPathFor(entry))
			if patchErr != nil && entry.OldPath != "" {
				patch, patchErr = c.Git.DiffPath(base, head, entry.OldPath)
			}
			if patchErr != nil {
				file.Omitted = true
				file.OmitReason = "file diff unavailable"
				diff.Warnings = append(diff.Warnings, fmt.Sprintf("failed to collect diff for %s: %v", fileTitle(file), patchErr))
			} else {
				file.Patch = patch
				applyModeHeaders(&file, patch)
			}
		}

		if file.Binary {
			file.Omitted = true
			file.OmitReason = firstReason(file.OmitReason, "binary file")
		}
		diff.Files = append(diff.Files, MarkGeneratedAndLarge(file, DefaultRenderOptions()))
	}

	return diff, nil
}

func (c LocalCollector) resolveRef(sha, ref, remote string, prNumber int) (string, error) {
	if sha != "" && c.hasCommit(sha) {
		return sha, nil
	}
	if ref != "" {
		_ = c.Git.FetchRef(remote, fmt.Sprintf("refs/heads/%s:refs/remotes/%s/%s", ref, remote, ref))
		if sha != "" && c.hasCommit(sha) {
			return sha, nil
		}
		remoteCandidate := "refs/remotes/" + remote + "/" + ref
		if resolved, err := c.Git.RevParse(remoteCandidate + "^{commit}"); err == nil {
			return resolved, nil
		}
	}
	if prNumber > 0 {
		if resolved, ok := c.resolvePullRequestHead(sha, remote, prNumber); ok {
			return resolved, nil
		}
	}
	if ref != "" {
		if resolved, err := c.Git.RevParse(ref + "^{commit}"); err == nil {
			return resolved, nil
		}
	}
	if sha != "" {
		return sha, nil
	}
	if ref != "" {
		return ref, nil
	}
	return "", fmt.Errorf("missing SHA or ref")
}

func (c LocalCollector) resolvePullRequestHead(sha, remote string, prNumber int) (string, bool) {
	_ = c.Git.FetchRef(remote, fmt.Sprintf("pull/%d/head:refs/remotes/%s/pr/%d", prNumber, remote, prNumber))
	if sha != "" && c.hasCommit(sha) {
		return sha, true
	}
	candidate := fmt.Sprintf("refs/remotes/%s/pr/%d^{commit}", remote, prNumber)
	if resolved, err := c.Git.RevParse(candidate); err == nil {
		return resolved, true
	}
	return "", false
}

func (c LocalCollector) hasCommit(ref string) bool {
	_, err := c.Git.RevParse(ref + "^{commit}")
	return err == nil
}

func changedFileFromNameStatus(entry git.NameStatusEntry) ChangedFile {
	file := ChangedFile{
		Path:    entry.Path,
		OldPath: entry.OldPath,
		Status:  statusFromNameStatus(entry.Status),
	}
	if file.Path == "" {
		file.Path = entry.OldPath
	}
	return file
}

func statusFromNameStatus(status string) ChangeStatus {
	if status == "" {
		return ChangeUnknown
	}
	switch status[0] {
	case 'A':
		return ChangeAdded
	case 'M':
		return ChangeModified
	case 'D':
		return ChangeDeleted
	case 'R':
		return ChangeRenamed
	case 'C':
		return ChangeCopied
	case 'T':
		return ChangeTypeChanged
	default:
		return ChangeUnknown
	}
}

func lookupNumStat(stats map[string][2]int, path, oldPath string) ([2]int, bool) {
	for _, candidate := range []string{path, oldPath} {
		if candidate == "" {
			continue
		}
		if stat, ok := stats[candidate]; ok {
			return stat, true
		}
	}
	return [2]int{}, false
}

func diffPathFor(entry git.NameStatusEntry) string {
	if entry.Path != "" {
		return entry.Path
	}
	return entry.OldPath
}

func applyModeHeaders(file *ChangedFile, patch string) {
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "old mode "):
			file.PreviousMode = strings.TrimPrefix(line, "old mode ")
		case strings.HasPrefix(line, "new mode "):
			file.CurrentMode = strings.TrimPrefix(line, "new mode ")
		case strings.HasPrefix(line, "deleted file mode "):
			file.PreviousMode = strings.TrimPrefix(line, "deleted file mode ")
		case strings.HasPrefix(line, "new file mode "):
			file.CurrentMode = strings.TrimPrefix(line, "new file mode ")
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
