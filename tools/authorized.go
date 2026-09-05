package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

// Authorization binds an already schema-validated proposal to runtime identity.
// Arguments and Description are ephemeral review previews for trusted host
// callbacks. Arguments may contain secrets: never persist or log these fields.
// Digest-only approvals.Request is the durable authorization record.
type Authorization struct {
	ToolBehavior                                runtime.ToolBehavior
	TaskID, SessionID, TurnID, AttemptID        string
	ToolCallID, ToolName, Scope                 string
	ArgumentsDigest, SchemaDigest, PolicyDigest string
	Arguments                                   json.RawMessage `json:"-"`
	Description                                 string          `json:"-"`
}

// ApprovalPrompt supplies the exact proposed arguments to a trusted operator
// review callback. Arguments may be sensitive and must never be persisted or
// logged. This private copy may be changed without changing execution arguments.
type ApprovalPrompt struct {
	Request     approvals.Request
	Arguments   json.RawMessage `json:"-"`
	Description string          `json:"-"`
}

// ApprovalReviewer authenticates the returned actor and asks the operator to
// approve this exact request. Callbacks must honor cancellation and never log
// raw Arguments. A returned actor name alone is not authentication.
type ApprovalReviewer func(context.Context, ApprovalPrompt) (actor string, allowed bool, err error)

// ApprovalPresenter presents an ephemeral approval preview without granting
// authority. An authenticated operator records the decision separately. The
// callback must honor cancellation and must not persist or log raw Arguments.
type ApprovalPresenter func(context.Context, ApprovalPrompt) error

// Authority is trusted host code, not model-controlled permission. It must
// obtain operator approval, consume durable authority under an active writer
// lease, cancel on lease loss, and retain ownership until handler returns.
// It must not retry a spent approval. A nil authority denies writes and Ask.
type Authority interface {
	ExecuteApproved(context.Context, Authorization, func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error)
}

func (e Executor) approved(ctx context.Context, x runtime.ToolExecution, t entry, policy *Policy, arguments json.RawMessage) (out runtime.ToolResult, err error) {
	out.Effect = runtime.NoEffect
	hash := func(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
	body, marshalErr := json.Marshal(struct {
		Version  int                  `json:"version"`
		Policy   *Policy              `json:"policy"`
		Behavior runtime.ToolBehavior `json:"behavior"`
	}{1, policy, t.Behavior})
	if marshalErr != nil {
		return out, ErrDenied
	}
	a := Authorization{TaskID: x.TaskID, SessionID: x.SessionID, TurnID: x.TurnID, AttemptID: x.AttemptID, ToolCallID: x.Call.ID, ToolName: x.Call.Name, Scope: t.Scope, ArgumentsDigest: hash(arguments), SchemaDigest: hash(t.Tool.Parameters), PolicyDigest: hash(body)}
	a.Arguments = append(json.RawMessage(nil), arguments...)
	a.ToolBehavior = t.Behavior
	a.Description = t.Tool.Description
	// Validate identities before a host authority or callback sees the proposal.
	now := time.Now().UTC()
	r := approvals.Request{Version: 1, ID: "validation", TaskID: a.TaskID, TurnID: a.TurnID, ToolCallID: a.ToolCallID, ToolName: a.ToolName, Scope: a.Scope, ArgumentsDigest: a.ArgumentsDigest, SchemaDigest: a.SchemaDigest, PolicyDigest: a.PolicyDigest, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	r.ToolBehavior = a.ToolBehavior
	if r.Validate() != nil {
		return out, ErrDenied
	}
	r.ID = a.SessionID
	if r.Validate() != nil {
		return out, ErrDenied
	}
	r.ID = a.AttemptID
	if r.Validate() != nil {
		return out, ErrDenied
	}
	var mu sync.Mutex
	closed, started := false, false
	var result runtime.ToolResult
	var handlerErr error
	// Join a started callback before this executor returns. The authority itself
	// must retain its lease until callback completion. Late/repeated calls fail shut.
	invoke := func(approvedCtx context.Context) (returned runtime.ToolResult, returnedErr error) {
		mu.Lock()
		defer mu.Unlock()
		if closed || started || approvedCtx == nil {
			return runtime.ToolResult{Effect: runtime.NoEffect}, ErrDenied
		}
		started = true
		result = runtime.ToolResult{Effect: runtime.UncertainEffect}
		defer func() {
			if recover() != nil {
				result = runtime.ToolResult{Effect: runtime.UncertainEffect}
				handlerErr = ErrExecution
				returned, returnedErr = result, handlerErr
			}
		}()
		runCtx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(approvedCtx, cancel)
		defer stop()
		defer cancel()
		if ctx.Err() != nil || approvedCtx.Err() != nil {
			handlerErr = ErrExecution
			return result, handlerErr
		}
		result, handlerErr = t.Handler(runCtx, arguments)
		if handlerErr != nil || runCtx.Err() != nil || approvedCtx.Err() != nil || len(result.Content) > 1<<20 || !utf8.ValidString(result.Content) || (result.Recoverable && (!result.Failed || result.Effect != runtime.NoEffect)) || (result.Effect != runtime.NoEffect && result.Effect != runtime.ConfirmedEffect) || (t.ReadOnly && result.Effect != runtime.NoEffect) {
			result = runtime.ToolResult{Effect: runtime.UncertainEffect}
			handlerErr = ErrExecution
		}
		return result, handlerErr
	}
	defer func() {
		panicked := recover() != nil
		mu.Lock()
		defer mu.Unlock()
		closed = true
		if panicked || (started && (err != nil || handlerErr != nil)) {
			out = runtime.ToolResult{Effect: runtime.UncertainEffect}
			err = ErrExecution
			return
		}
		if !started {
			// A durable consumption acknowledgement may be uncertain even when
			// the callback was not entered. Never downgrade that recovery signal.
			if err != nil && out.Effect == runtime.UncertainEffect {
				out = runtime.ToolResult{Effect: runtime.UncertainEffect}
				err = ErrExecution
				return
			}
			out = runtime.ToolResult{Effect: runtime.NoEffect}
			err = ErrDenied
			return
		}
		// Do not trust an authority to replace the handler's output or effect.
		out, err = result, handlerErr
	}()
	return e.Authority.ExecuteApproved(ctx, a, invoke)
}
