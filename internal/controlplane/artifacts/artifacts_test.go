package artifacts

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidate(t *testing.T) {
	patch := []byte("diff --git a/file.txt b/file.txt\n")
	metadata := BuildMetadata("acme/widgets", "job-1", "base", "head", "patches/job.patch", patch)

	tests := []struct {
		name      string
		mutate    func(*PatchMetadata, map[string][]byte)
		wantError string
	}{
		{
			name: "valid",
		},
		{
			name: "unsupported version",
			mutate: func(metadata *PatchMetadata, _ map[string][]byte) {
				metadata.Version = 2
			},
			wantError: "unsupported patch metadata version",
		},
		{
			name: "unsupported format",
			mutate: func(metadata *PatchMetadata, _ map[string][]byte) {
				metadata.Format = "zip"
			},
			wantError: "unsupported patch artifact format",
		},
		{
			name: "repository mismatch",
			mutate: func(metadata *PatchMetadata, _ map[string][]byte) {
				metadata.Repository = "acme/other"
			},
			wantError: "patch repository does not match result repository",
		},
		{
			name: "job mismatch",
			mutate: func(metadata *PatchMetadata, _ map[string][]byte) {
				metadata.JobID = "job-2"
			},
			wantError: "patch job_id does not match result job_id",
		},
		{
			name: "base mismatch",
			mutate: func(metadata *PatchMetadata, _ map[string][]byte) {
				metadata.BaseSHA = "old"
			},
			wantError: "patch base SHA does not match result base SHA",
		},
		{
			name: "head mismatch",
			mutate: func(metadata *PatchMetadata, _ map[string][]byte) {
				metadata.ExpectedHeadSHA = "old"
			},
			wantError: "patch expected head SHA does not match result expected head SHA",
		},
		{
			name: "invalid checksum",
			mutate: func(metadata *PatchMetadata, _ map[string][]byte) {
				metadata.SHA256 = SHA256([]byte("other"))
			},
			wantError: "patch artifact checksum mismatch",
		},
		{
			name: "missing artifact",
			mutate: func(metadata *PatchMetadata, artifacts map[string][]byte) {
				delete(artifacts, metadata.ArtifactName)
			},
			wantError: "unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMetadata := metadata
			files := map[string][]byte{
				"metadata.json":          mustJSON(t, gotMetadata),
				gotMetadata.ArtifactName: patch,
			}
			if tt.mutate != nil {
				tt.mutate(&gotMetadata, files)
				files["metadata.json"] = mustJSON(t, gotMetadata)
			}
			got, err := Validate(context.Background(), memoryArtifactStore(files), ValidationRequest{
				Repository:       "acme/widgets",
				JobID:            "job-1",
				BaseSHA:          "base",
				ExpectedHeadSHA:  "head",
				MetadataArtifact: "metadata.json",
			})
			if tt.wantError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantError)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, metadata, got.Metadata)
			assert.Equal(t, patch, got.Data)
		})
	}
}

func TestValidateBundledPatchArtifact(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*PatchMetadata, map[string][]byte)
		wantError string
	}{
		{
			name: "valid",
		},
		{
			name: "missing metadata",
			mutate: func(_ *PatchMetadata, files map[string][]byte) {
				delete(files, bundledMetadataFile)
			},
			wantError: "patch metadata",
		},
		{
			name: "checksum mismatch",
			mutate: func(metadata *PatchMetadata, _ map[string][]byte) {
				metadata.SHA256 = SHA256([]byte("other"))
			},
			wantError: "patch artifact checksum mismatch",
		},
		{
			name: "missing patch",
			mutate: func(metadata *PatchMetadata, files map[string][]byte) {
				delete(files, metadata.ArtifactName)
			},
			wantError: "missing from artifact bundle",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patch := []byte("diff --git a/file.txt b/file.txt\n")
			metadata := BuildMetadata("acme/widgets", "job-1", "base", "head", "herd-worker.patch", patch)
			files := map[string][]byte{
				bundledMetadataFile:   mustJSON(t, metadata),
				metadata.ArtifactName: patch,
			}
			if tt.mutate != nil {
				tt.mutate(&metadata, files)
				if _, ok := files[bundledMetadataFile]; ok {
					files[bundledMetadataFile] = mustJSON(t, metadata)
				}
			}
			got, err := Validate(context.Background(), memoryArtifactStore{
				"worker-branch": zipArtifact(t, files),
			}, ValidationRequest{
				Repository:       "acme/widgets",
				JobID:            "job-1",
				BaseSHA:          "base",
				ExpectedHeadSHA:  "head",
				MetadataArtifact: "worker-branch",
			})
			if tt.wantError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantError)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, metadata, got.Metadata)
			assert.Equal(t, patch, got.Data)
		})
	}
}

func TestValidateRejectsOversizedArtifacts(t *testing.T) {
	tests := []struct {
		name          string
		files         memoryArtifactStore
		metadataName  string
		wantErrorPart string
	}{
		{
			name: "metadata",
			files: memoryArtifactStore{
				"metadata.json": bytes.Repeat([]byte(" "), maxMetadataBytes+1),
			},
			metadataName:  "metadata.json",
			wantErrorPart: "maximum size",
		},
		{
			name: "patch",
			files: func() memoryArtifactStore {
				patch := bytes.Repeat([]byte("x"), maxPatchBytes+1)
				metadata := BuildMetadata("acme/widgets", "job-1", "base", "head", "patch.diff", patch)
				return memoryArtifactStore{
					"metadata.json": mustJSON(t, metadata),
					"patch.diff":    patch,
				}
			}(),
			metadataName:  "metadata.json",
			wantErrorPart: "maximum size",
		},
		{
			name: "zipped patch entry",
			files: func() memoryArtifactStore {
				patch := bytes.Repeat([]byte("x"), maxPatchBytes+1)
				metadata := BuildMetadata("acme/widgets", "job-1", "base", "head", "patch.diff", patch)
				return memoryArtifactStore{
					"worker-branch": zipArtifact(t, map[string][]byte{
						bundledMetadataFile: mustJSON(t, metadata),
						"patch.diff":        patch,
					}),
				}
			}(),
			metadataName:  "worker-branch",
			wantErrorPart: "maximum size",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Validate(context.Background(), tt.files, ValidationRequest{
				Repository:       "acme/widgets",
				JobID:            "job-1",
				BaseSHA:          "base",
				ExpectedHeadSHA:  "head",
				MetadataArtifact: tt.metadataName,
			})

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErrorPart)
		})
	}
}

func TestArtifactRepositoryContextRoundTrip(t *testing.T) {
	ctx := ContextWithArtifactRepository(context.Background(), "acme/widgets", 99)

	got := ArtifactRepositoryFromContext(ctx)

	assert.Equal(t, "acme/widgets", got.Repository)
	assert.Equal(t, int64(99), got.InstallationID)
	assert.Empty(t, ArtifactRepositoryFromContext(context.Background()))
}

func TestGitHubActionsStoreRequiresTokenSource(t *testing.T) {
	_, err := GitHubActionsStore{}.OpenArtifact(ContextWithArtifactRepository(context.Background(), "acme/widgets", 99), "worker-branch")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "token source")
}

type memoryArtifactStore map[string][]byte

func (s memoryArtifactStore) OpenArtifact(_ context.Context, name string) (io.ReadCloser, error) {
	data, ok := s[name]
	if !ok {
		return nil, fmt.Errorf("missing artifact")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return data
}

func zipArtifact(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}
