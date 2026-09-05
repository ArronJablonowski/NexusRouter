package app

import (
	"context"
	"strings"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/internal/toolgate"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

type toolAuthority struct {
	gate    *toolgate.Gate
	secrets []string
}

func newToolAuthority(db *telemetry.Store, reviewer tools.ApprovalReviewer, presenter tools.ApprovalPresenter, secrets []string) tools.Authority {
	a := &toolAuthority{secrets: append([]string(nil), secrets...)}
	a.gate = &toolgate.Gate{Store: db, Present: presenter}
	if reviewer != nil {
		a.gate.ReviewPrompt = func(ctx context.Context, p tools.ApprovalPrompt) (string, bool, error) {
			actor, allowed, err := reviewer(ctx, p)
			// Actor attribution is persisted verbatim. Reject credentials rather than
			// silently changing the claimed operator identity through redaction.
			if a.containsSecret(actor) {
				return "", false, tools.ErrDenied
			}
			return actor, allowed, err
		}
	}
	return a
}

func (a *toolAuthority) containsSecret(value string) bool {
	for _, secret := range a.secrets {
		if secret != "" && strings.Contains(value, secret) {
			return true
		}
	}
	return false
}

func (a *toolAuthority) ExecuteApproved(ctx context.Context, proposal tools.Authorization, handler func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
	// Scope and tool identity are durable approval bindings and cannot be
	// redacted without changing authority. Reject configured secrets up front.
	for _, identity := range []string{proposal.Scope, proposal.ToolName, proposal.ToolCallID, proposal.TaskID, proposal.SessionID, proposal.TurnID, proposal.AttemptID} {
		if a.containsSecret(identity) {
			return runtime.ToolResult{Effect: runtime.NoEffect}, tools.ErrDenied
		}
	}
	return a.gate.ExecuteApproved(ctx, proposal, handler)
}
