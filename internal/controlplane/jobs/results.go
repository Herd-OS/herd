package jobs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const (
	ResultVersion = 1

	KindWorkerCompleted = "worker_completed"
	KindReviewCompleted = "review_completed"

	StatusSuccess          = "success"
	StatusFailure          = "failure"
	StatusApproved         = "approved"
	StatusChangesRequested = "changes_requested"
	StatusMaxCyclesHit     = "max_cycles_hit"
)

type ResultEnvelope struct {
	Version    int    `json:"version"`
	Kind       string `json:"kind"`
	Repository string `json:"repository"`
	JobID      string `json:"job_id"`
}

type WorkerCompletedResult struct {
	Version         int    `json:"version"`
	Kind            string `json:"kind"`
	Repository      string `json:"repository"`
	JobID           string `json:"job_id"`
	BatchNumber     int    `json:"batch_number"`
	IssueNumber     int    `json:"issue_number"`
	TargetBranch    string `json:"target_branch"`
	BaseSHA         string `json:"base_sha"`
	ExpectedHeadSHA string `json:"expected_head_sha"`
	PatchArtifact   string `json:"patch_artifact"`
	Status          string `json:"status"`
}

type ReviewCompletedResult struct {
	Version     int    `json:"version"`
	Kind        string `json:"kind"`
	Repository  string `json:"repository"`
	JobID       string `json:"job_id"`
	BatchNumber int    `json:"batch_number"`
	PRNumber    int    `json:"pr_number"`
	HeadSHA     string `json:"head_sha"`
	Status      string `json:"status"`
	Summary     string `json:"summary"`
}

type Result interface {
	Envelope() ResultEnvelope
	StatusValue() string
	ResultHeadSHA() string
}

func (r WorkerCompletedResult) Envelope() ResultEnvelope {
	return ResultEnvelope{Version: r.Version, Kind: r.Kind, Repository: r.Repository, JobID: r.JobID}
}

func (r WorkerCompletedResult) StatusValue() string {
	return r.Status
}

func (r WorkerCompletedResult) ResultHeadSHA() string {
	return r.ExpectedHeadSHA
}

func (r ReviewCompletedResult) Envelope() ResultEnvelope {
	return ResultEnvelope{Version: r.Version, Kind: r.Kind, Repository: r.Repository, JobID: r.JobID}
}

func (r ReviewCompletedResult) StatusValue() string {
	return r.Status
}

func (r ReviewCompletedResult) ResultHeadSHA() string {
	return r.HeadSHA
}

func ParseResultPayload(payload []byte) (Result, error) {
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil, fmt.Errorf("result payload is empty")
	}

	var envelope ResultEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, fmt.Errorf("malformed JSON result payload")
	}
	if err := validateEnvelope(envelope); err != nil {
		return nil, err
	}

	switch envelope.Kind {
	case KindWorkerCompleted:
		var result WorkerCompletedResult
		if err := decodeStrict(payload, &result); err != nil {
			return nil, err
		}
		return result, validateWorkerCompleted(result)
	case KindReviewCompleted:
		var result ReviewCompletedResult
		if err := decodeStrict(payload, &result); err != nil {
			return nil, err
		}
		return result, validateReviewCompleted(result)
	default:
		return nil, fmt.Errorf("unsupported result kind %q", envelope.Kind)
	}
}

func ResultPayloadHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func ResultIdempotencyKey(result Result, payload []byte) string {
	return result.Envelope().Kind + ":" + ResultPayloadHash(payload)
}

func validateEnvelope(envelope ResultEnvelope) error {
	if envelope.Version != ResultVersion {
		return fmt.Errorf("unsupported result version %d", envelope.Version)
	}
	if strings.TrimSpace(envelope.Kind) == "" {
		return fmt.Errorf("result kind is required")
	}
	if envelope.Kind != KindWorkerCompleted && envelope.Kind != KindReviewCompleted {
		return fmt.Errorf("unsupported result kind %q", envelope.Kind)
	}
	if strings.TrimSpace(envelope.Repository) == "" {
		return fmt.Errorf("repository is required")
	}
	if strings.TrimSpace(envelope.JobID) == "" {
		return fmt.Errorf("job_id is required")
	}
	return nil
}

func validateWorkerCompleted(result WorkerCompletedResult) error {
	if err := validateEnvelope(result.Envelope()); err != nil {
		return err
	}
	if result.BatchNumber <= 0 {
		return fmt.Errorf("batch_number is required")
	}
	if result.IssueNumber <= 0 {
		return fmt.Errorf("issue_number is required")
	}
	if strings.TrimSpace(result.TargetBranch) == "" {
		return fmt.Errorf("target_branch is required")
	}
	if strings.TrimSpace(result.BaseSHA) == "" {
		return fmt.Errorf("base_sha is required")
	}
	if strings.TrimSpace(result.ExpectedHeadSHA) == "" {
		return fmt.Errorf("expected_head_sha is required")
	}
	if strings.TrimSpace(result.PatchArtifact) == "" {
		return fmt.Errorf("patch_artifact is required")
	}
	if !validWorkerStatus(result.Status) {
		return fmt.Errorf("invalid worker result status %q", result.Status)
	}
	return nil
}

func validateReviewCompleted(result ReviewCompletedResult) error {
	if err := validateEnvelope(result.Envelope()); err != nil {
		return err
	}
	if result.BatchNumber <= 0 {
		return fmt.Errorf("batch_number is required")
	}
	if result.PRNumber <= 0 {
		return fmt.Errorf("pr_number is required")
	}
	if strings.TrimSpace(result.HeadSHA) == "" {
		return fmt.Errorf("head_sha is required")
	}
	if strings.TrimSpace(result.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	if !validReviewStatus(result.Status) {
		return fmt.Errorf("invalid review result status %q", result.Status)
	}
	return nil
}

func validWorkerStatus(status string) bool {
	switch status {
	case StatusSuccess, StatusFailure, StatusMaxCyclesHit:
		return true
	default:
		return false
	}
}

func validReviewStatus(status string) bool {
	switch status {
	case StatusApproved, StatusChangesRequested, StatusFailure:
		return true
	default:
		return false
	}
}

func decodeStrict(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid result payload: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("invalid result payload: multiple JSON values")
	}
	return nil
}
