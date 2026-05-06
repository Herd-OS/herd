package dashboard

import "strings"

// TierProgressGlyphs returns a glyph string for the supplied counts using the
// order: done (●), in-progress (◐), ready/blocked (○), failed (✗). The total
// number of glyphs equals done+inProgress+ready+failed.
func TierProgressGlyphs(done, inProgress, ready, failed int) string {
	var b strings.Builder
	b.Grow(done + inProgress + ready + failed)
	for i := 0; i < done; i++ {
		b.WriteRune('●')
	}
	for i := 0; i < inProgress; i++ {
		b.WriteRune('◐')
	}
	for i := 0; i < ready; i++ {
		b.WriteRune('○')
	}
	for i := 0; i < failed; i++ {
		b.WriteRune('✗')
	}
	return b.String()
}
