package reviewdiff

import "github.com/herd-os/herd/internal/platform"

func FromPlatformFiles(prNumber int, baseSHA string, headSHA string, files []*platform.PullRequestFile) DiffSet {
	diff := DiffSet{
		PRNumber: prNumber,
		BaseSHA:  baseSHA,
		HeadSHA:  headSHA,
		Source:   "github-files-api",
		Files:    make([]ChangedFile, 0, len(files)),
	}
	for _, file := range files {
		if file == nil {
			continue
		}
		changed := ChangedFile{
			Path:      file.Path,
			OldPath:   file.PreviousPath,
			Status:    fromPlatformFileStatus(file.Status),
			Additions: file.Additions,
			Deletions: file.Deletions,
			Changes:   file.Changes,
			Patch:     file.Patch,
		}
		if changed.Patch == "" {
			switch {
			case changed.Additions == 0 && changed.Deletions == 0:
				changed.Binary = true
				changed.Omitted = true
				changed.OmitReason = "binary file"
			case changed.Changes != 0:
				changed.Omitted = true
				changed.OmitReason = "patch unavailable from GitHub files API"
			}
		}
		diff.Files = append(diff.Files, MarkGeneratedAndLarge(changed, DefaultRenderOptions()))
	}
	return diff
}

func fromPlatformFileStatus(status string) ChangeStatus {
	switch status {
	case "added":
		return ChangeAdded
	case "modified", "changed":
		return ChangeModified
	case "removed":
		return ChangeDeleted
	case "renamed":
		return ChangeRenamed
	default:
		return ChangeUnknown
	}
}
