package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const (
	MetadataVersion = 1

	FormatGitDiffBinary = "git-diff-binary"
)

type PatchMetadata struct {
	Version         int    `json:"version"`
	Repository      string `json:"repository"`
	JobID           string `json:"job_id"`
	BaseSHA         string `json:"base_sha"`
	ExpectedHeadSHA string `json:"expected_head_sha"`
	Format          string `json:"format"`
	ArtifactName    string `json:"artifact_name"`
	SHA256          string `json:"sha256"`
}

type Store interface {
	OpenArtifact(ctx context.Context, name string) (io.ReadCloser, error)
}

type ValidationRequest struct {
	Repository       string
	JobID            string
	BaseSHA          string
	ExpectedHeadSHA  string
	MetadataArtifact string
}

type ValidatedArtifact struct {
	Metadata PatchMetadata
	Data     []byte
}

func Validate(ctx context.Context, store Store, req ValidationRequest) (ValidatedArtifact, error) {
	if store == nil {
		return ValidatedArtifact{}, fmt.Errorf("artifact store is required")
	}
	if strings.TrimSpace(req.MetadataArtifact) == "" {
		return ValidatedArtifact{}, fmt.Errorf("patch artifact is required")
	}
	metadataBytes, err := readArtifact(ctx, store, req.MetadataArtifact)
	if err != nil {
		return ValidatedArtifact{}, fmt.Errorf("metadata artifact %q unavailable: %w", req.MetadataArtifact, err)
	}
	var metadata PatchMetadata
	decoder := json.NewDecoder(bytes.NewReader(metadataBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return ValidatedArtifact{}, fmt.Errorf("invalid patch metadata: %w", err)
	}
	if err := validateMetadata(metadata, req); err != nil {
		return ValidatedArtifact{}, err
	}
	data, err := readArtifact(ctx, store, metadata.ArtifactName)
	if err != nil {
		return ValidatedArtifact{}, fmt.Errorf("patch artifact %q unavailable: %w", metadata.ArtifactName, err)
	}
	if got := SHA256(data); !strings.EqualFold(got, metadata.SHA256) {
		return ValidatedArtifact{}, fmt.Errorf("patch artifact checksum mismatch: expected %s, got %s", metadata.SHA256, got)
	}
	return ValidatedArtifact{Metadata: metadata, Data: data}, nil
}

func BuildMetadata(repository, jobID, baseSHA, expectedHeadSHA, artifactName string, data []byte) PatchMetadata {
	return PatchMetadata{
		Version:         MetadataVersion,
		Repository:      repository,
		JobID:           jobID,
		BaseSHA:         baseSHA,
		ExpectedHeadSHA: expectedHeadSHA,
		Format:          FormatGitDiffBinary,
		ArtifactName:    artifactName,
		SHA256:          SHA256(data),
	}
}

func SHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validateMetadata(metadata PatchMetadata, req ValidationRequest) error {
	if metadata.Version != MetadataVersion {
		return fmt.Errorf("unsupported patch metadata version %d", metadata.Version)
	}
	if metadata.Format != FormatGitDiffBinary {
		return fmt.Errorf("unsupported patch artifact format %q", metadata.Format)
	}
	if metadata.Repository != req.Repository {
		return fmt.Errorf("patch repository does not match result repository")
	}
	if metadata.JobID != req.JobID {
		return fmt.Errorf("patch job_id does not match result job_id")
	}
	if metadata.BaseSHA != req.BaseSHA {
		return fmt.Errorf("patch base SHA does not match result base SHA")
	}
	if metadata.ExpectedHeadSHA != req.ExpectedHeadSHA {
		return fmt.Errorf("patch expected head SHA does not match result expected head SHA")
	}
	if strings.TrimSpace(metadata.ArtifactName) == "" {
		return fmt.Errorf("patch artifact name is required")
	}
	if strings.TrimSpace(metadata.SHA256) == "" {
		return fmt.Errorf("patch artifact checksum is required")
	}
	if len(metadata.SHA256) != 64 {
		return fmt.Errorf("patch artifact checksum must be SHA-256 hex")
	}
	return nil
}

func readArtifact(ctx context.Context, store Store, name string) ([]byte, error) {
	rc, err := store.OpenArtifact(ctx, name)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rc.Close()
	}()
	return io.ReadAll(rc)
}
