package reviewdiff

const (
	DefaultMaxTotalDiffBytes        = 180000
	DefaultMaxFileDiffBytes         = 40000
	DefaultMaxIncludedFiles         = 80
	DefaultMaxOmittedSummaryEntries = 100
	LargeLockfileDiffBytes          = 20000
)

type ChangeStatus string

const (
	ChangeAdded       ChangeStatus = "added"
	ChangeModified    ChangeStatus = "modified"
	ChangeDeleted     ChangeStatus = "deleted"
	ChangeRenamed     ChangeStatus = "renamed"
	ChangeCopied      ChangeStatus = "copied"
	ChangeTypeChanged ChangeStatus = "type_changed"
	ChangeUnknown     ChangeStatus = "unknown"
)

type ChangedFile struct {
	Path         string
	OldPath      string
	Status       ChangeStatus
	Additions    int
	Deletions    int
	Changes      int
	Patch        string
	Binary       bool
	Generated    bool
	Large        bool
	Truncated    bool
	Omitted      bool
	OmitReason   string
	PreviousMode string
	CurrentMode  string
}

type DiffSet struct {
	PRNumber int
	BaseSHA  string
	HeadSHA  string
	Source   string
	Files    []ChangedFile
	Warnings []string
}

type RenderOptions struct {
	MaxTotalDiffBytes        int
	MaxFileDiffBytes         int
	MaxIncludedFiles         int
	MaxOmittedSummaryEntries int
}

type RenderResult struct {
	Text           string
	IncludedFiles  []ChangedFile
	OmittedFiles   []ChangedFile
	TruncatedFiles []ChangedFile
	Warnings       []string
	WasLimited     bool
}

func DefaultRenderOptions() RenderOptions {
	return RenderOptions{
		MaxTotalDiffBytes:        DefaultMaxTotalDiffBytes,
		MaxFileDiffBytes:         DefaultMaxFileDiffBytes,
		MaxIncludedFiles:         DefaultMaxIncludedFiles,
		MaxOmittedSummaryEntries: DefaultMaxOmittedSummaryEntries,
	}
}

func normalizeOptions(opts RenderOptions) RenderOptions {
	defaults := DefaultRenderOptions()
	if opts.MaxTotalDiffBytes <= 0 {
		opts.MaxTotalDiffBytes = defaults.MaxTotalDiffBytes
	}
	if opts.MaxFileDiffBytes <= 0 {
		opts.MaxFileDiffBytes = defaults.MaxFileDiffBytes
	}
	if opts.MaxIncludedFiles <= 0 {
		opts.MaxIncludedFiles = defaults.MaxIncludedFiles
	}
	if opts.MaxOmittedSummaryEntries <= 0 {
		opts.MaxOmittedSummaryEntries = defaults.MaxOmittedSummaryEntries
	}
	return opts
}
