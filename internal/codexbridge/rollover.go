package codexbridge

import (
	"context"
	"reflect"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

// CheckContextRollover reports whether a completed native turn can be replaced
// by a newly imported conversation. It is deliberately read-only: the caller
// must activate the replacement through the application-owned generation
// boundary, which creates the next native session.
func (s *Session) CheckContextRollover(ctx context.Context, current, prospectiveReplacement providers.Request) error {
	if !s.mu.TryLock() {
		return failure(false)
	}
	defer s.mu.Unlock()

	if ctx == nil || ctx.Err() != nil || s.closed.Load() || !s.started || !s.finished ||
		s.pending != nil || !validOpaque(s.thread) || !validOpaque(s.turn) ||
		s.completedRequest == nil || !reflect.DeepEqual(current, *s.completedRequest) {
		return failure(false)
	}
	if current.Model != prospectiveReplacement.Model ||
		!reflect.DeepEqual(current.Tools, prospectiveReplacement.Tools) ||
		!reflect.DeepEqual(current.JSONSchema, prospectiveReplacement.JSONSchema) ||
		current.MaxOutputTokens != prospectiveReplacement.MaxOutputTokens {
		return failure(false)
	}
	if !validRequest(prospectiveReplacement) || prospectiveReplacement.Model != s.options.Model ||
		providers.ValidateMessages(prospectiveReplacement.Messages) != nil ||
		ValidateInitialMessages(prospectiveReplacement.Messages) != nil {
		return failure(false)
	}
	if ctx.Err() != nil {
		return failure(false)
	}
	return nil
}
