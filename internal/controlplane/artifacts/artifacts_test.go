package artifacts

import (
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
