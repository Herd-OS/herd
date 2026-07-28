package batchmeta

import (
	"encoding/json"
	"strings"

	"github.com/herd-os/herd/internal/platform"
)

const (
	markerPrefix = "<!-- herd:batch-metadata "
	markerSuffix = " -->"
)

const CurrentVersion = 1

type Metadata struct {
	Version   int    `json:"version"`
	PRSummary string `json:"pr_summary,omitempty"`
}

func Append(description string, metadata Metadata) (string, error) {
	metadata.PRSummary = strings.TrimSpace(metadata.PRSummary)
	if metadata.PRSummary == "" {
		return strings.TrimSpace(description), nil
	}
	if metadata.Version == 0 {
		metadata.Version = CurrentVersion
	}

	payload, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}

	marker := markerPrefix + string(payload) + markerSuffix
	description = strings.TrimSpace(description)
	if description == "" {
		return marker, nil
	}

	return description + "\n\n" + marker, nil
}

func Parse(description string) (Metadata, bool) {
	start := strings.Index(description, markerPrefix)
	if start < 0 {
		return Metadata{}, false
	}

	payloadStart := start + len(markerPrefix)
	endOffset := strings.Index(description[payloadStart:], markerSuffix)
	if endOffset < 0 {
		return Metadata{}, false
	}

	var metadata Metadata
	if err := json.Unmarshal([]byte(description[payloadStart:payloadStart+endOffset]), &metadata); err != nil {
		return Metadata{}, false
	}
	if metadata.Version != CurrentVersion {
		return Metadata{}, false
	}

	metadata.PRSummary = strings.TrimSpace(metadata.PRSummary)
	if metadata.PRSummary == "" {
		return Metadata{}, false
	}

	return Metadata{Version: CurrentVersion, PRSummary: metadata.PRSummary}, true
}

func MilestonePRSummary(ms *platform.Milestone) string {
	if ms == nil {
		return ""
	}

	if metadata, ok := Parse(ms.Description); ok {
		return metadata.PRSummary
	}

	return strings.TrimSpace(stripMarker(ms.Description))
}

func stripMarker(description string) string {
	start := strings.Index(description, markerPrefix)
	if start < 0 {
		return description
	}

	endOffset := strings.Index(description[start+len(markerPrefix):], markerSuffix)
	if endOffset < 0 {
		return description[:start]
	}

	end := start + len(markerPrefix) + endOffset + len(markerSuffix)
	return description[:start] + description[end:]
}
