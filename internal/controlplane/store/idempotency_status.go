package store

import (
	"strings"

	"github.com/herd-os/herd/internal/controlplane/mutations"
)

// idempotencyFailureStatus maps boundary-tagged failures into first-class
// durable phases. GitHub-visible mutation callers use these phases to decide
// whether a retry is still before the external side effect or now requires
// repair/inspection. Untagged failures remain legacy failed records.
func idempotencyFailureStatus(errorMessage string) (status string, resultRef string) {
	message := strings.TrimSpace(errorMessage)
	for _, phase := range []string{mutations.PhaseFailedPreCall, mutations.PhaseRepairRequired} {
		prefix := phase + ":"
		if message == phase {
			return phase, ""
		}
		if strings.HasPrefix(message, prefix) {
			return phase, strings.TrimSpace(strings.TrimPrefix(message, prefix))
		}
	}
	return mutations.LegacyFailed, errorMessage
}
