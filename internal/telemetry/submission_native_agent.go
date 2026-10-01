package telemetry

import (
	"context"
	"time"

	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// RecoverInterruptedNativeAgentCommitScreened fences an expired native owner and
// commits failure/cancellation without repeating inference or tool effects. The
// shared transaction preserves leases and records an explicit recovery receipt.
func (s *Store) RecoverInterruptedNativeAgentCommitScreened(ctx context.Context, id, configDigest string, now time.Time, secrets []string) (SubmissionRecoveryCommit, error) {
	return s.recoverInterruptedSubmissionCommit(ctx, id, configDigest, now, "interrupted_native_agent", 1, sessions.PlanInterruptedNativeAgent, secrets)
}
