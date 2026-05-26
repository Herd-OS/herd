package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRunnerImageTag(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{"released version", "v1.4.2", "v1.4.2"},
		{"dev build", "dev", "latest"},
		{"empty", "", "latest"},
		{"whitespace", "  v2.0.0  ", "v2.0.0"},
		{"whitespace dev", " dev ", "latest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, runnerImageTag(tt.version))
		})
	}
}
