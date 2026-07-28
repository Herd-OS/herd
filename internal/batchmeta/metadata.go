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
	for searchStart := 0; searchStart < len(description); {
		startOffset := strings.Index(description[searchStart:], markerPrefix)
		if startOffset < 0 {
			return Metadata{}, false
		}

		payloadStart := searchStart + startOffset + len(markerPrefix)
		remaining := description[payloadStart:]
		endOffset := strings.Index(remaining, markerSuffix)
		nextStartOffset := strings.Index(remaining, markerPrefix)
		if endOffset < 0 {
			if nextStartOffset >= 0 {
				searchStart = payloadStart + nextStartOffset
				continue
			}
			return Metadata{}, false
		}
		if nextStartOffset >= 0 && nextStartOffset < endOffset {
			searchStart = payloadStart + nextStartOffset
			continue
		}

		searchStart = payloadStart + endOffset + len(markerSuffix)

		var metadata Metadata
		if err := json.Unmarshal([]byte(remaining[:endOffset]), &metadata); err != nil {
			continue
		}
		if metadata.Version != CurrentVersion {
			continue
		}

		metadata.PRSummary = strings.TrimSpace(metadata.PRSummary)
		if metadata.PRSummary == "" {
			continue
		}

		return Metadata{Version: CurrentVersion, PRSummary: metadata.PRSummary}, true
	}

	return Metadata{}, false
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
	var b strings.Builder
	searchStart := 0

	for searchStart < len(description) {
		startOffset := strings.Index(description[searchStart:], markerPrefix)
		if startOffset < 0 {
			b.WriteString(description[searchStart:])
			return b.String()
		}

		start := searchStart + startOffset
		b.WriteString(description[searchStart:start])

		payloadStart := start + len(markerPrefix)
		endOffset := strings.Index(description[payloadStart:], markerSuffix)
		if endOffset < 0 {
			return b.String()
		}

		searchStart = payloadStart + endOffset + len(markerSuffix)
	}

	return b.String()
}
