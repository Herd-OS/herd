package dashboard

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClampRefresh(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"zero", 0, 5},
		{"below min", 4, 5},
		{"at min", 5, 5},
		{"in range", 15, 15},
		{"at max", 300, 300},
		{"above max", 301, 300},
		{"way above max", 999, 300},
		{"negative", -10, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ClampRefresh(tt.in))
		})
	}
}
