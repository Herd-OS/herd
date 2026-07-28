package batchmeta

import (
	"strings"
	"testing"

	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppend(t *testing.T) {
	tests := []struct {
		name        string
		description string
		metadata    Metadata
		want        string
	}{
		{
			name:     "empty description returns marker only",
			metadata: Metadata{PRSummary: "Adds auth"},
			want:     `<!-- herd:batch-metadata {"version":1,"pr_summary":"Adds auth"} -->`,
		},
		{
			name:        "existing description is kept with blank line before marker",
			description: "Legacy prose",
			metadata:    Metadata{PRSummary: "Adds auth"},
			want:        "Legacy prose\n\n" + `<!-- herd:batch-metadata {"version":1,"pr_summary":"Adds auth"} -->`,
		},
		{
			name:        "empty summary returns trimmed description",
			description: " Legacy prose \n",
			metadata:    Metadata{PRSummary: ""},
			want:        "Legacy prose",
		},
		{
			name:     "empty summary and empty description returns empty",
			metadata: Metadata{PRSummary: ""},
			want:     "",
		},
		{
			name:        "whitespace summary returns trimmed description",
			description: " Legacy prose \n",
			metadata:    Metadata{PRSummary: " \n\t "},
			want:        "Legacy prose",
		},
		{
			name:     "keeps explicit non-zero version",
			metadata: Metadata{Version: CurrentVersion, PRSummary: "Adds auth"},
			want:     `<!-- herd:batch-metadata {"version":1,"pr_summary":"Adds auth"} -->`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Append(tt.description, tt.metadata)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			if strings.TrimSpace(tt.metadata.PRSummary) == "" {
				assert.NotContains(t, got, markerPrefix)
			}
		})
	}
}

func TestAppendEscapingRoundTrip(t *testing.T) {
	summary := "Quote: \"hello\"\n* Markdown `code` & <script>alert('x')</script> > end"

	description, err := Append("", Metadata{PRSummary: " \n" + summary + "\t "})
	require.NoError(t, err)

	assert.Contains(t, description, `\"hello\"`)
	assert.Contains(t, description, `\n`)
	assert.Contains(t, description, `\u003cscript\u003e`)
	assert.NotContains(t, description, "<script>")

	metadata, ok := Parse(description)
	require.True(t, ok)
	assert.Equal(t, Metadata{Version: CurrentVersion, PRSummary: summary}, metadata)
}

func TestParse(t *testing.T) {
	validMarker := `<!-- herd:batch-metadata {"version":1,"pr_summary":"Adds auth"} -->`

	tests := []struct {
		name        string
		description string
		want        Metadata
		wantOK      bool
	}{
		{
			name:        "finds marker at beginning",
			description: validMarker,
			want:        Metadata{Version: CurrentVersion, PRSummary: "Adds auth"},
			wantOK:      true,
		},
		{
			name:        "finds marker in middle",
			description: "before\n\n" + validMarker + "\n\nafter",
			want:        Metadata{Version: CurrentVersion, PRSummary: "Adds auth"},
			wantOK:      true,
		},
		{
			name: "skips closed malformed marker before valid marker",
			description: "before\n\n" +
				`<!-- herd:batch-metadata {bad} -->` +
				"\n\nmiddle\n\n" +
				validMarker +
				"\n\nafter",
			want:   Metadata{Version: CurrentVersion, PRSummary: "Adds auth"},
			wantOK: true,
		},
		{
			name: "skips closed blank marker before valid marker",
			description: `<!-- herd:batch-metadata {"version":1,"pr_summary":" "} -->` +
				"\n\n" +
				validMarker,
			want:   Metadata{Version: CurrentVersion, PRSummary: "Adds auth"},
			wantOK: true,
		},
		{
			name: "skips unsupported version before valid marker",
			description: `<!-- herd:batch-metadata {"version":2,"pr_summary":"Old summary"} -->` +
				"\n\n" +
				validMarker,
			want:   Metadata{Version: CurrentVersion, PRSummary: "Adds auth"},
			wantOK: true,
		},
		{
			name:        "trims summary",
			description: `<!-- herd:batch-metadata {"version":1,"pr_summary":"  Adds auth \n"} -->`,
			want:        Metadata{Version: CurrentVersion, PRSummary: "Adds auth"},
			wantOK:      true,
		},
		{
			name:        "absent marker",
			description: "Legacy prose",
		},
		{
			name:        "malformed json",
			description: `<!-- herd:batch-metadata {"version":1,"pr_summary": -->`,
		},
		{
			name:        "missing close suffix",
			description: `<!-- herd:batch-metadata {"version":1,"pr_summary":"Adds auth"}`,
		},
		{
			name: "skips unterminated marker before valid marker",
			description: `<!-- herd:batch-metadata {"version":1,"pr_summary":"Broken"` +
				"\n\n" +
				validMarker,
			want:   Metadata{Version: CurrentVersion, PRSummary: "Adds auth"},
			wantOK: true,
		},
		{
			name:        "unsupported version",
			description: `<!-- herd:batch-metadata {"version":2,"pr_summary":"Adds auth"} -->`,
		},
		{
			name:        "blank summary",
			description: `<!-- herd:batch-metadata {"version":1,"pr_summary":" \n\t "} -->`,
		},
		{
			name:        "missing summary",
			description: `<!-- herd:batch-metadata {"version":1} -->`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Parse(tt.description)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMilestonePRSummary(t *testing.T) {
	structuredMarker := `<!-- herd:batch-metadata {"version":1,"pr_summary":"Structured summary"} -->`
	malformedMarker := `<!-- herd:batch-metadata {"version":1,"pr_summary": -->`

	tests := []struct {
		name string
		ms   *platform.Milestone
		want string
	}{
		{
			name: "nil milestone",
			want: "",
		},
		{
			name: "structured summary",
			ms:   &platform.Milestone{Description: structuredMarker},
			want: "Structured summary",
		},
		{
			name: "legacy plain description",
			ms:   &platform.Milestone{Description: " Legacy prose \n"},
			want: "Legacy prose",
		},
		{
			name: "structured metadata wins over surrounding prose",
			ms:   &platform.Milestone{Description: "Plain prose\n\n" + structuredMarker},
			want: "Structured summary",
		},
		{
			name: "structured metadata wins after closed malformed marker and prose",
			ms: &platform.Milestone{Description: "Before\n\n" +
				malformedMarker +
				"\n\nMiddle prose\n\n" +
				structuredMarker +
				"\n\nAfter"},
			want: "Structured summary",
		},
		{
			name: "closed malformed marker falls back to surrounding prose",
			ms:   &platform.Milestone{Description: "Before\n\n" + malformedMarker + "\n\nAfter"},
			want: "Before\n\n\n\nAfter",
		},
		{
			name: "multiple closed invalid markers are stripped from fallback prose",
			ms: &platform.Milestone{Description: "Plain\n\n" +
				`<!-- herd:batch-metadata {bad} -->` +
				"\n\n" +
				`<!-- herd:batch-metadata {"version":1,"pr_summary":" "} -->`},
			want: "Plain",
		},
		{
			name: "closed malformed marker only returns empty",
			ms:   &platform.Milestone{Description: malformedMarker},
			want: "",
		},
		{
			name: "unterminated malformed marker only returns empty",
			ms:   &platform.Milestone{Description: `<!-- herd:batch-metadata {"version":1`},
			want: "",
		},
		{
			name: "unterminated malformed marker after prose removes marker fragment",
			ms:   &platform.Milestone{Description: `Plain prose <!-- herd:batch-metadata {"version":1`},
			want: "Plain prose",
		},
		{
			name: "whitespace description returns empty",
			ms:   &platform.Milestone{Description: " \n\t "},
			want: "",
		},
		{
			name: "blank structured marker only returns empty",
			ms:   &platform.Milestone{Description: `<!-- herd:batch-metadata {"version":1,"pr_summary":" "} -->`},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MilestonePRSummary(tt.ms)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, markerPrefix)
			assert.NotContains(t, got, markerSuffix)
		})
	}
}
