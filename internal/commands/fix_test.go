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

func TestLooksLikeConflict(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"merge conflict lowercase", "there is a merge conflict", true},
		{"merge conflict mixed case", "Merge Conflict detected", true},
		{"rebase conflict", "Rebase conflict on main", true},
		{"conflict with main", "PR has conflict with main", true},
		{"conflict with master", "conflict with master branch", true},
		{"conflicts with main", "this conflicts with main", true},
		{"conflicts with master", "this conflicts with master", true},
		{"no conflict keywords", "fix the broken test", false},
		{"empty string", "", false},
		{"partial match - conflict alone", "there is a conflict", false},
		{"partial match - merge alone", "merge the branches", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, looksLikeConflict(tc.input))
		})
	}
}

func TestAppendConflictInstructions(t *testing.T) {
	body := "original body"
	result := appendConflictInstructions(body, "herd/batch/1-test")

	assert.Contains(t, result, "original body")
	assert.Contains(t, result, "## Git Instructions")
	assert.Contains(t, result, "merge conflict")
	assert.Contains(t, result, "rebase conflict")
	assert.Contains(t, result, "git merge origin/herd/batch/1-test")
	assert.Contains(t, result, "git rebase origin/main")
	assert.Contains(t, result, "Do NOT rewrite files from scratch")
	assert.Contains(t, result, "git rebase --continue")
}
