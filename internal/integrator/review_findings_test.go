package integrator

import (
	"testing"

	"github.com/herd-os/herd/internal/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDedupeReviewFindings(t *testing.T) {
	tests := []struct {
		name           string
		findings       []agent.ReviewFinding
		wantDescs      []string
		wantSeverities []string
		wantDeduped    int
		wantFingerSame bool
	}{
		{
			name: "pr level stale cascade wording variations collapse",
			findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "Repository owner feedback says Herd cascade-failed because of an unresolved merge conflict in this chunk."},
				{Severity: " high ", Description: "Previous review says the herd cascade failed due to merge conflicts in chunk 2/3."},
				{Severity: "HIGH", Description: "Cycle 3 still reports a branch conflict in the conflict resolution cascade."},
			},
			wantDescs: []string{
				"Repository owner feedback says Herd cascade-failed because of an unresolved merge conflict in this chunk.",
			},
			wantDeduped:    2,
			wantFingerSame: true,
		},
		{
			name: "same file path symbol and claim collapse",
			findings: []agent.ReviewFinding{
				{Severity: "MEDIUM", Description: "cmd/herd-service/main.go:123: function runServer misses the nil check before using cfg."},
				{Severity: "medium", Description: "Visible diff: cmd/herd-service/main.go func runServer missing nil check before using cfg."},
			},
			wantDescs: []string{
				"cmd/herd-service/main.go:123: function runServer misses the nil check before using cfg.",
			},
			wantDeduped:    1,
			wantFingerSame: true,
		},
		{
			name: "same file path symbol and claim collapse across severity and keep higher severity",
			findings: []agent.ReviewFinding{
				{Severity: "MEDIUM", Description: "internal/integrator/review.go:123: function runReview misses the nil check before using result."},
				{Severity: "HIGH", Description: "internal/integrator/review.go:456: function runReview misses the nil check before using result."},
			},
			wantDescs: []string{
				"internal/integrator/review.go:456: function runReview misses the nil check before using result.",
			},
			wantSeverities: []string{"HIGH"},
			wantDeduped:    1,
			wantFingerSame: true,
		},
		{
			name: "same fingerprint keeps complete description and highest severity",
			findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "internal/integrator/review.go: function runReview missing nil check before using result."},
				{Severity: "MEDIUM", Description: "Visible diff: internal/integrator/review.go:123: function runReview misses the nil check before using result."},
			},
			wantDescs: []string{
				"Visible diff: internal/integrator/review.go:123: function runReview misses the nil check before using result.",
			},
			wantSeverities: []string{"HIGH"},
			wantDeduped:    1,
			wantFingerSame: true,
		},
		{
			name: "distinct claims in same file remain separate",
			findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "internal/integrator/review.go:123: missing nil check before dereferencing result."},
				{Severity: "HIGH", Description: "internal/integrator/review.go:456: returns before recording the chunk stats."},
			},
			wantDescs: []string{
				"internal/integrator/review.go:123: missing nil check before dereferencing result.",
				"internal/integrator/review.go:456: returns before recording the chunk stats.",
			},
		},
		{
			name: "same path but different symbol remains separate",
			findings: []agent.ReviewFinding{
				{Severity: "HIGH", Description: "internal/integrator/review.go: function runReview missing nil check."},
				{Severity: "HIGH", Description: "internal/integrator/review.go: function runChunkedReviewWithRetry missing nil check."},
			},
			wantDescs: []string{
				"internal/integrator/review.go: function runReview missing nil check.",
				"internal/integrator/review.go: function runChunkedReviewWithRetry missing nil check.",
			},
		},
		{
			name: "whitespace only duplicates collapse",
			findings: []agent.ReviewFinding{
				{Severity: "LOW", Description: "Small cleanup in docs."},
				{Severity: " low ", Description: "  Small   cleanup in docs.  "},
			},
			wantDescs: []string{
				"Small cleanup in docs.",
			},
			wantDeduped:    1,
			wantFingerSame: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, stats := dedupeReviewFindings(tt.findings)

			require.Len(t, got, len(tt.wantDescs))
			descs := make([]string, 0, len(got))
			for _, finding := range got {
				descs = append(descs, finding.Description)
			}
			assert.Equal(t, tt.wantDescs, descs)
			if tt.wantSeverities != nil {
				severities := make([]string, 0, len(got))
				for _, finding := range got {
					severities = append(severities, finding.Severity)
				}
				assert.Equal(t, tt.wantSeverities, severities)
			}
			assert.Equal(t, len(tt.findings), stats.RawFindings)
			assert.Equal(t, len(tt.wantDescs), stats.FindingsAfterDedupe)
			assert.Equal(t, tt.wantDeduped, stats.DedupedFindings)
			if tt.wantFingerSame {
				require.GreaterOrEqual(t, len(tt.findings), 2)
				assert.Equal(t, reviewFindingFingerprint(tt.findings[0]), reviewFindingFingerprint(tt.findings[1]))
			}
		})
	}
}

func TestReviewFindingExtractionAndSelection(t *testing.T) {
	pathTests := []struct {
		name string
		desc string
		want string
	}{
		{name: "path prefix strips line", desc: "cmd/herd-service/main.go:123: missing validation", want: "cmd/herd-service/main.go"},
		{name: "root go mod path", desc: "go.mod: merge conflict marker dependency resolution is wrong", want: "go.mod"},
		{name: "root package json path", desc: "package.json: resolve merge conflicts script ignores errors", want: "package.json"},
		{name: "root settings go path", desc: "settings.go: unresolved conflict handler drops errors", want: "settings.go"},
		{name: "embedded yaml path", desc: "The workflow .github/workflows/review.yml:42 has stale config", want: ".github/workflows/review.yml"},
		{name: "typescript path prefix", desc: "web/src/mergeResolver.ts: resolve merge conflicts ignores errors", want: "web/src/mergeresolver.ts"},
		{name: "python embedded path", desc: "The handler in scripts/merge_resolver.py:77 drops errors", want: "scripts/merge_resolver.py"},
		{name: "no path", desc: "PR-level merge conflict remains unresolved", want: ""},
	}
	for _, tt := range pathTests {
		t.Run("path "+tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, extractFindingPath(tt.desc))
		})
	}

	symbolTests := []struct {
		name string
		desc string
		want string
	}{
		{name: "function keyword", desc: "function runServer drops the error", want: "runserver"},
		{name: "method keyword", desc: "method Worker.Run ignores ctx", want: "worker.run"},
		{name: "call syntax", desc: "RunReview() can panic", want: "runreview"},
		{name: "none", desc: "the file-level path is wrong", want: ""},
	}
	for _, tt := range symbolTests {
		t.Run("symbol "+tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, extractFindingSymbol(tt.desc))
		})
	}

	short := agent.ReviewFinding{Severity: "HIGH", Description: "missing nil check"}
	complete := agent.ReviewFinding{Severity: "LOW", Description: "internal/integrator/review.go:123: function runReview is missing a nil check before dereferencing result"}
	got := betterFinding(short, complete)
	assert.Equal(t, complete.Description, got.Description)
	assert.Equal(t, short.Severity, got.Severity)

	low := agent.ReviewFinding{Severity: "LOW", Description: "same text"}
	high := agent.ReviewFinding{Severity: "HIGH", Description: "same text"}
	assert.Equal(t, high, betterFinding(low, high))
	assert.Equal(t, "cfg check cmd/app/main.go missing nil", compactFindingClaim("cmd/app/main.go:12: cfg missing nil check in this chunk"))
}
