package dashboard

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTierProgressGlyphs(t *testing.T) {
	tests := []struct {
		name                                string
		done, inProgress, ready, failed     int
		want                                string
	}{
		{"all zero", 0, 0, 0, 0, ""},
		{"only done", 3, 0, 0, 0, "●●●"},
		{"only in-progress", 0, 2, 0, 0, "◐◐"},
		{"only ready", 0, 0, 4, 0, "○○○○"},
		{"only failed", 0, 0, 0, 2, "✗✗"},
		{"mixed (5,2,2,1)", 5, 2, 2, 1, "●●●●●◐◐○○✗"},
		{"single each", 1, 1, 1, 1, "●◐○✗"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TierProgressGlyphs(tt.done, tt.inProgress, tt.ready, tt.failed)
			assert.Equal(t, tt.want, got)
		})
	}
}
