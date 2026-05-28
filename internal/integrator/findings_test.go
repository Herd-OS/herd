package integrator

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/herd-os/herd/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractFindings_BodyPlusHumanComments(t *testing.T) {
	body := "---\nherd:\n  version: 1\n---\n\nOriginal manual issue body.\n\nMore detail here."
	comments := []*platform.Comment{
		{ID: 1, Body: "First human comment.", AuthorLogin: "alice"},
		{ID: 2, Body: "Second human comment.", AuthorLogin: "alice"},
		{ID: 3, Body: "Hello from a bot.", AuthorLogin: "herd-os[bot]"},
	}

	result, ok := extractFindings(42, body, comments)

	require.True(t, ok)
	assert.Contains(t, result, "Original manual issue body.")
	assert.Contains(t, result, "More detail here.")
	assert.Contains(t, result, "First human comment.")
	assert.Contains(t, result, "Second human comment.")
	assert.NotContains(t, result, "Hello from a bot.")
	// Frontmatter is stripped.
	assert.NotContains(t, result, "herd:")
	assert.NotContains(t, result, "version: 1")
}

func TestExtractFindings_FiltersHerdAutomatedComments(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"integrator warning", "⚠️ **HerdOS Integrator**\n\nSomething happened."},
		{"wrench prefix", "🔧 fix automation comment"},
		{"worker progress final", "📋 **Worker Progress** _(final)_\n\nstuff"},
		{"worker progress live", "📋 **Worker Progress** _(live)_\n\nstuff"},
		{"worker report broad", "📋 **Worker #42 Report**\n\nstuff"},
		{"review pending", "🔍 **HerdOS Agent Review**\n\nfindings"},
		{"review ok", "✅ **HerdOS Agent Review**\n\nlooks good"},
		{"integrator generic", "🔄 **Integrator**\n\nupdate"},
		{"manual task ping", "👋 **Manual task** — this requires human action."},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			comments := []*platform.Comment{
				{ID: 1, Body: "Real human note.", AuthorLogin: "alice"},
				{ID: 2, Body: tc.body, AuthorLogin: "alice"},
			}

			result, ok := extractFindings(7, "Body text.", comments)

			require.True(t, ok)
			assert.Contains(t, result, "Body text.")
			assert.Contains(t, result, "Real human note.")
			assert.NotContains(t, result, tc.body)
		})
	}
}

func TestExtractFindings_OnlyAutomatedYieldsBodyOnly(t *testing.T) {
	comments := []*platform.Comment{
		{ID: 1, Body: "⚠️ **HerdOS Integrator**\n\nautomated only", AuthorLogin: "alice"},
	}
	result, ok := extractFindings(11, "Body text.", comments)
	require.True(t, ok)
	assert.Equal(t, "Body text.", result)
}

func TestExtractFindings_TruncatesLargeContent(t *testing.T) {
	large := strings.Repeat("x", perDepFindingsCap*2)
	comments := []*platform.Comment{
		{ID: 1, Body: large, AuthorLogin: "alice"},
	}

	result, ok := extractFindings(99, "", comments)

	require.True(t, ok)
	notice := fmt.Sprintf("\n\n_...truncated; see #%d for full findings._", 99)
	assert.LessOrEqual(t, len(result), perDepFindingsCap+len(notice))
	assert.Contains(t, result, "truncated; see #99")
	assert.True(t, strings.HasSuffix(result, notice))
}

func TestExtractFindings_TruncationKeepsUTF8Valid(t *testing.T) {
	// "🐑" is 4 bytes; building a long string of these ensures cutting at the
	// rune-unaware byte cap would land mid-rune without runeBoundaryBefore.
	large := strings.Repeat("🐑", perDepFindingsCap)
	comments := []*platform.Comment{
		{ID: 1, Body: large, AuthorLogin: "alice"},
	}

	result, ok := extractFindings(5, "", comments)

	require.True(t, ok)
	assert.True(t, utf8.ValidString(result), "result must be valid UTF-8")
	assert.Contains(t, result, "truncated; see #5")
}

func TestExtractFindings_OnlyBoilerplate_ReturnsEmpty(t *testing.T) {
	// Only frontmatter — StripFrontMatter yields empty markdown.
	body := "---\nherd:\n  version: 1\n---\n"

	result, ok := extractFindings(3, body, nil)

	assert.False(t, ok)
	assert.Equal(t, "", result)
}

func TestExtractFindings_AllCommentsEmpty_ReturnsEmpty(t *testing.T) {
	comments := []*platform.Comment{
		{ID: 1, Body: "   \n  \t", AuthorLogin: "alice"},
		{ID: 2, Body: "", AuthorLogin: "bob"},
	}
	result, ok := extractFindings(3, "", comments)
	assert.False(t, ok)
	assert.Equal(t, "", result)
}

func TestInjectFindings_AppendsBlock(t *testing.T) {
	body := "## Existing content\n\nSome description."
	out, changed := injectFindings(body, 10, "Findings text from #10.")

	require.True(t, changed)
	assert.Contains(t, out, injectedFindingsMarker(10))
	assert.Contains(t, out, "## Context from #10 (manual task)")
	assert.Contains(t, out, "Findings text from #10.")
	assert.Contains(t, out, injectedFindingsCloseMarker(10))
	// Original body is preserved.
	assert.Contains(t, out, "## Existing content")
	assert.Contains(t, out, "Some description.")
}

func TestInjectFindings_Idempotent(t *testing.T) {
	body := "Body text.\n\n" + injectedFindingsMarker(10) + "\n## Context from #10 (manual task)\n\nExisting findings.\n" + injectedFindingsCloseMarker(10) + "\n"

	out, changed := injectFindings(body, 10, "New findings — should not be appended.")

	assert.False(t, changed)
	assert.Equal(t, body, out)
	assert.NotContains(t, out, "New findings")
}

func TestInjectFindings_MultipleSources(t *testing.T) {
	body := "Original body."

	out, changed := injectFindings(body, 10, "Findings from #10.")
	require.True(t, changed)
	assert.Contains(t, out, injectedFindingsMarker(10))

	out2, changed2 := injectFindings(out, 11, "Findings from #11.")
	require.True(t, changed2)
	assert.Contains(t, out2, injectedFindingsMarker(10))
	assert.Contains(t, out2, injectedFindingsMarker(11))
	assert.Contains(t, out2, "Findings from #10.")
	assert.Contains(t, out2, "Findings from #11.")

	// Re-injecting #10 is a no-op.
	out3, changed3 := injectFindings(out2, 10, "Different findings text for #10.")
	assert.False(t, changed3)
	assert.Equal(t, out2, out3)
	assert.Equal(t, 1, strings.Count(out3, injectedFindingsMarker(10)))
	assert.Equal(t, 1, strings.Count(out3, injectedFindingsMarker(11)))
}
