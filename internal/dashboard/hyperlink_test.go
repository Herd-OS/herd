package dashboard

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHyperlink(t *testing.T) {
	tests := []struct {
		name string
		url  string
		text string
		want string
	}{
		{
			name: "emits OSC 8",
			url:  "https://example.com",
			text: "hello",
			want: "\x1b]8;;https://example.com\x1b\\hello\x1b]8;;\x1b\\",
		},
		{
			name: "empty url returns plain text",
			url:  "",
			text: "hello",
			want: "hello",
		},
		{
			name: "empty url and empty text",
			url:  "",
			text: "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Hyperlink(tt.url, tt.text)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestHyperlink_EmitsOSC8(t *testing.T) {
	got := Hyperlink("https://example.com", "hello")
	assert.Equal(t, "\x1b]8;;https://example.com\x1b\\hello\x1b]8;;\x1b\\", got)
}
