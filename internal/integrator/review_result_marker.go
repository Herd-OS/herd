package integrator

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/herd-os/herd/internal/platform"
)

const reviewResultMarkerPrefix = "<!-- herd:review-result "
const reviewResultMarkerSuffix = " -->"

const (
	reviewResultStatusApproved         = "approved"
	reviewResultStatusChangesRequested = "changes_requested"
	reviewResultStatusMaxCyclesHit     = "max_cycles_hit"
)

type reviewResultMarker struct {
	Version       int       `json:"version"`
	PRNumber      int       `json:"pr_number"`
	BatchNumber   int       `json:"batch_number"`
	HeadSHA       string    `json:"head_sha"`
	Status        string    `json:"status"`
	Cycle         int       `json:"cycle,omitempty"`
	FindingsCount int       `json:"findings_count"`
	CreatedAt     time.Time `json:"created_at"`
}

func buildReviewResultMarker(m reviewResultMarker) (string, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshaling review result marker: %w", err)
	}
	return reviewResultMarkerPrefix + string(data) + reviewResultMarkerSuffix, nil
}

func parseReviewResultMarker(body string) (reviewResultMarker, bool) {
	start := strings.Index(body, reviewResultMarkerPrefix)
	if start < 0 {
		return reviewResultMarker{}, false
	}
	start += len(reviewResultMarkerPrefix)
	end := strings.Index(body[start:], reviewResultMarkerSuffix)
	if end < 0 {
		return reviewResultMarker{}, false
	}

	raw := strings.TrimSpace(body[start : start+end])
	var marker reviewResultMarker
	if err := json.Unmarshal([]byte(raw), &marker); err != nil {
		return reviewResultMarker{}, false
	}
	if marker.Version != 1 ||
		marker.PRNumber == 0 ||
		marker.BatchNumber == 0 ||
		marker.HeadSHA == "" ||
		!validReviewResultStatus(marker.Status) ||
		marker.CreatedAt.IsZero() {
		return reviewResultMarker{}, false
	}
	return marker, true
}

func validReviewResultStatus(status string) bool {
	switch status {
	case reviewResultStatusApproved, reviewResultStatusChangesRequested, reviewResultStatusMaxCyclesHit:
		return true
	default:
		return false
	}
}

func latestReviewResultMarker(comments []*platform.Comment, prNumber, batchNumber int, headSHA string, trustedHumanLogins ...string) (reviewResultMarker, bool) {
	var latest reviewResultMarker
	found := false
	for _, comment := range comments {
		if !isTrustedReviewResultMarkerComment(comment, trustedHumanLogins...) {
			continue
		}
		marker, ok := parseReviewResultMarker(comment.Body)
		if !ok {
			continue
		}
		if marker.PRNumber != prNumber || marker.BatchNumber != batchNumber || marker.HeadSHA != headSHA {
			continue
		}
		if !found || marker.CreatedAt.After(latest.CreatedAt) {
			latest = marker
			found = true
		}
	}
	return latest, found
}

func isTrustedReviewResultMarkerComment(comment *platform.Comment, trustedHumanLogins ...string) bool {
	if comment == nil {
		return false
	}
	if strings.HasSuffix(comment.AuthorLogin, "[bot]") {
		return true
	}
	for _, login := range trustedHumanLogins {
		if login != "" && comment.AuthorLogin == login {
			return true
		}
	}
	return false
}

func appendReviewResultMarker(comment string, marker reviewResultMarker) (string, error) {
	markerBody, err := buildReviewResultMarker(marker)
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimRight(comment, " \t\r\n")
	if trimmed == "" {
		return markerBody, nil
	}
	return trimmed + "\n\n" + markerBody, nil
}

func newReviewResultMarker(prNumber, batchNumber int, headSHA, status string, cycle, findingsCount int, now time.Time) reviewResultMarker {
	return reviewResultMarker{
		Version:       1,
		PRNumber:      prNumber,
		BatchNumber:   batchNumber,
		HeadSHA:       headSHA,
		Status:        status,
		Cycle:         cycle,
		FindingsCount: findingsCount,
		CreatedAt:     now.UTC(),
	}
}
