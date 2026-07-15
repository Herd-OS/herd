package artifacts

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

const (
	MetadataVersion = 1

	FormatGitDiffBinary = "git-diff-binary"

	bundledMetadataFile = "herd-worker-metadata.json"

	maxMetadataBytes = 1 << 20
	maxPatchBytes    = 64 << 20
	maxBundleBytes   = 128 << 20
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
	metadataBytes, err := readArtifact(ctx, store, req.MetadataArtifact, maxBundleBytes)
	if err != nil {
		return ValidatedArtifact{}, fmt.Errorf("metadata artifact %q unavailable: %w", req.MetadataArtifact, err)
	}
	if artifact, ok, bundleErr := validateBundle(metadataBytes, req); ok || bundleErr != nil {
		return artifact, bundleErr
	}
	if len(metadataBytes) > maxMetadataBytes {
		return ValidatedArtifact{}, fmt.Errorf("patch metadata exceeds maximum size")
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
	data, err := readArtifact(ctx, store, metadata.ArtifactName, maxPatchBytes)
	if err != nil {
		return ValidatedArtifact{}, fmt.Errorf("patch artifact %q unavailable: %w", metadata.ArtifactName, err)
	}
	if got := SHA256(data); !strings.EqualFold(got, metadata.SHA256) {
		return ValidatedArtifact{}, fmt.Errorf("patch artifact checksum mismatch: expected %s, got %s", metadata.SHA256, got)
	}
	return ValidatedArtifact{Metadata: metadata, Data: data}, nil
}

func validateBundle(data []byte, req ValidationRequest) (ValidatedArtifact, bool, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return ValidatedArtifact{}, false, nil
	}
	files := map[string][]byte{}
	total := 0
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		limit := maxPatchBytes
		name := strings.TrimPrefix(filepath.ToSlash(file.Name), "./")
		if name == bundledMetadataFile || filepath.Base(name) == bundledMetadataFile {
			limit = maxMetadataBytes
		}
		body, err := readZipFile(file, limit)
		if err != nil {
			return ValidatedArtifact{}, true, err
		}
		total += len(body)
		if total > maxBundleBytes {
			return ValidatedArtifact{}, true, fmt.Errorf("patch artifact bundle exceeds maximum size")
		}
		files[name] = body
		files[filepath.Base(name)] = body
	}
	metadataBytes, ok := files[bundledMetadataFile]
	if !ok {
		return ValidatedArtifact{}, true, fmt.Errorf("patch metadata %q missing from artifact bundle", bundledMetadataFile)
	}
	var metadata PatchMetadata
	decoder := json.NewDecoder(bytes.NewReader(metadataBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return ValidatedArtifact{}, true, fmt.Errorf("invalid patch metadata: %w", err)
	}
	if err := validateMetadata(metadata, req); err != nil {
		return ValidatedArtifact{}, true, err
	}
	patchName := strings.TrimPrefix(filepath.ToSlash(metadata.ArtifactName), "./")
	patch, ok := files[patchName]
	if !ok {
		patch, ok = files[filepath.Base(patchName)]
	}
	if !ok {
		return ValidatedArtifact{}, true, fmt.Errorf("patch artifact %q missing from artifact bundle", metadata.ArtifactName)
	}
	if got := SHA256(patch); !strings.EqualFold(got, metadata.SHA256) {
		return ValidatedArtifact{}, true, fmt.Errorf("patch artifact checksum mismatch: expected %s, got %s", metadata.SHA256, got)
	}
	return ValidatedArtifact{Metadata: metadata, Data: patch}, true, nil
}

func readZipFile(file *zip.File, limit int) ([]byte, error) {
	if file.UncompressedSize64 > uint64(limit) { //nolint:gosec // limit is a positive internal artifact size cap.
		return nil, fmt.Errorf("zip entry %q exceeds maximum size", file.Name)
	}
	rc, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rc.Close()
	}()
	return readAllLimited(rc, limit)
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

func readArtifact(ctx context.Context, store Store, name string, limit int) ([]byte, error) {
	rc, err := store.OpenArtifact(ctx, name)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rc.Close()
	}()
	return readAllLimited(rc, limit)
}

func readAllLimited(r io.Reader, limit int) ([]byte, error) {
	limited := io.LimitReader(r, int64(limit)+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("artifact exceeds maximum size")
	}
	return data, nil
}
