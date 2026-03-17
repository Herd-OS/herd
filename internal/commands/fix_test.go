package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		n     int
		want  string
	}{
		{
			name:  "short ASCII string unchanged",
			input: "hello",
			n:     60,
			want:  "hello",
		},
		{
			name:  "exactly n runes unchanged",
			input: "abcde",
			n:     5,
			want:  "abcde",
		},
		{
			name:  "longer than n ASCII string truncated",
			input: "this is a very long string that exceeds sixty characters for sure yes indeed",
			n:     60,
			want:  "this is a very long string that exceeds sixty characters for" + "...",
		},
		{
			name:  "multi-byte UTF-8 string truncated safely",
			input: "日本語のテキストはマルチバイト文字を含むためバイト境界での切り捨ては危険です",
			n:     10,
			want:  "日本語のテキストはマ" + "...",
		},
		{
			name:  "multi-byte UTF-8 string shorter than n unchanged",
			input: "こんにちは",
			n:     60,
			want:  "こんにちは",
		},
		{
			name:  "empty string unchanged",
			input: "",
			n:     60,
			want:  "",
		},
		{
			name:  "mixed ASCII and multi-byte truncated at rune boundary",
			input: "hello 世界 world more text here",
			n:     8,
			want:  "hello 世界" + "...",
		},
		{
			name:  "n=0 always truncates non-empty",
			input: "abc",
			n:     0,
			want:  "...",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateRunes(tc.input, tc.n)
			assert.Equal(t, tc.want, got)
		})
	}
}
