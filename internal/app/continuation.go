package app

import (
	"context"

	"darwinrouter/providers"
	"darwinrouter/runtime"
	"darwinrouter/sessions"
)

type continuationContext struct {
	Messages           []providers.Message
	SessionID, Privacy string
	Compaction         *runtime.ContextCompaction
}

func loadContinuation(ctx context.Context, db sessions.Reader, r Request, secrets []string) (*continuationContext, error) {
	history, err := sessions.Replay(ctx, db, r.ContinueTaskID)
	if err != nil || history.State != "completed" || history.InterruptedTurn || history.UncertainEffects || len(history.Pending) > 0 {
		return nil, ErrAdmission
	}
	result := &continuationContext{Messages: history.Messages, SessionID: history.SessionID, Privacy: history.Privacy}
	if r.Compaction != nil {
		// Redact before both provider assembly and checkpoint persistence.
		request := *r.Compaction
		fields := []*[]string{&request.Summary.Decisions, &request.Summary.PendingWork, &request.Summary.Failures, &request.Summary.Artifacts}
		for _, field := range fields {
			copy := append([]string(nil), (*field)...)
			for i := range copy {
				copy[i] = redact(copy[i], secrets)
			}
			*field = copy
		}
		result.Messages, result.Compaction, err = sessions.PrepareContinuation(history, request)
		if err != nil {
			return nil, ErrAdmission
		}
	}
	return result, nil
}
