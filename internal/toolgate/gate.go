// Package toolgate binds operator approvals to durable, single-use tool work.
package toolgate

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

// Gate is configured by trusted host code and must remain immutable during use.
// Review authenticates its returned actor; a name alone is not authentication.
// Review and handlers are cooperative in-process callbacks, not sandboxed code.
// They must honor cancellation, and handlers must join any work they start
// before returning. A canceled callback is never abandoned or retried.
type Gate struct {
	Store *telemetry.Store
	// Review is the legacy metadata-only hook. Configure exactly one reviewer.
	Review func(context.Context, approvals.Request) (actor string, allowed bool, err error)
	// ReviewPrompt receives an isolated, ephemeral argument preview. It must not
	// log secrets or persist raw arguments; the ledger contains only digests.
	ReviewPrompt tools.ApprovalReviewer
	// Present displays the preview then waits for a separately recorded operator
	// decision. Exactly one of Review, ReviewPrompt, or Present may be configured.
	Present tools.ApprovalPresenter
}

var _ tools.Authority = (*Gate)(nil)

// ExecuteApproved persists review and consumes authority before invoking handler.
// It deliberately cannot resume an earlier approval: duplicate calls fail closed.
func (g *Gate) ExecuteApproved(ctx context.Context, a tools.Authorization, handler func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
	noEffect := runtime.ToolResult{Effect: runtime.NoEffect}
	if g == nil || g.Store == nil || !g.oneReviewer() || handler == nil || ctx.Err() != nil {
		return noEffect, tools.ErrDenied
	}
	// Legacy metadata fixtures may omit arguments. Preview-based approval never
	// permits an absent, mismatched, oversized, or malformed preview.
	var arguments json.RawMessage
	if g.ReviewPrompt != nil || g.Present != nil || a.Arguments != nil {
		if len(a.Arguments) > 1<<20 || !utf8.Valid(a.Arguments) || !json.Valid(a.Arguments) || len(a.Description) > 4096 || !utf8.ValidString(a.Description) {
			return noEffect, tools.ErrDenied
		}
		arguments = append(json.RawMessage(nil), a.Arguments...)
		digest := sha256.Sum256(arguments)
		if hex.EncodeToString(digest[:]) != a.ArgumentsDigest {
			return noEffect, tools.ErrDenied
		}
	}
	now := time.Now().UTC()
	req := approvals.Request{Version: approvals.Version, ID: rand.Text(), TaskID: a.TaskID, TurnID: a.TurnID, ToolCallID: a.ToolCallID, ToolName: a.ToolName, ToolBehavior: a.ToolBehavior, Scope: a.Scope, ArgumentsDigest: a.ArgumentsDigest, SchemaDigest: a.SchemaDigest, PolicyDigest: a.PolicyDigest, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if req.Validate() != nil {
		return noEffect, tools.ErrDenied
	}
	snapshot, err := g.Store.TaskSnapshot(ctx, a.TaskID)
	pending := snapshot.Pending[a.ToolCallID]
	if err != nil || snapshot.SessionID != a.SessionID || pending.AttemptID != a.AttemptID || pending.ToolBehavior != a.ToolBehavior || a.AttemptID == "" || a.SessionID == "" {
		return noEffect, tools.ErrDenied
	}
	if _, err = g.Store.RequestApproval(ctx, req); err != nil {
		return noEffect, tools.ErrDenied
	}
	reviewCtx, cancel := context.WithDeadline(ctx, req.ExpiresAt)
	var actor string
	var allowed bool
	if g.Present != nil {
		err = present(reviewCtx, g.Present, tools.ApprovalPrompt{Request: req, Arguments: arguments, Description: a.Description})
		if err == nil {
			err = g.awaitDecision(reviewCtx, req)
		}
	} else if g.ReviewPrompt != nil {
		actor, allowed, err = reviewPrompt(reviewCtx, g.ReviewPrompt, tools.ApprovalPrompt{Request: req, Arguments: arguments, Description: a.Description})
	} else {
		actor, allowed, err = review(reviewCtx, g.Review, req)
	}
	late := reviewCtx.Err() != nil
	cancel()
	if err != nil || late {
		return noEffect, tools.ErrDenied
	}
	if g.Present == nil {
		decision := approvals.Decision{ID: rand.Text(), Actor: actor, Allowed: allowed, Time: time.Now().UTC()}
		if _, err = g.Store.DecideApproval(ctx, req.ID, decision); err != nil || !allowed {
			return noEffect, tools.ErrDenied
		}
	}
	owner := rand.Text()
	lease, err := g.Store.AcquireLease(ctx, a.TaskID, owner, a.Scope, true, time.Now().UTC(), 30*time.Second)
	if err != nil {
		return noEffect, tools.ErrDenied
	}
	// Even an ambiguous consumption acknowledgement cannot authorize a retry.
	consumed, consumeErr := g.Store.ConsumeApproval(ctx, req, lease.Token, owner, time.Now().UTC())
	if consumeErr != nil {
		g.release(lease.Token, owner)
		return runtime.ToolResult{Effect: runtime.UncertainEffect}, tools.ErrExecution
	}
	workCtx, stop := context.WithCancel(ctx)
	workCtx = tools.WithConsumedApproval(workCtx, tools.ConsumedApproval{ID: consumed.Request.ID})
	done := make(chan struct{})
	renewed := make(chan error, 1)
	go func() { renewed <- g.renew(workCtx, stop, done, a.TaskID, lease.Token, owner) }()
	var out runtime.ToolResult
	// Consumption may wait for a database lock after its supplied timestamp was
	// captured. Do not dispatch on observably expired authority or ownership.
	if dispatchWindow(workCtx, time.Now().UTC(), req.ExpiresAt, lease.Expires) {
		out, err = invoke(workCtx, handler)
	} else {
		err = tools.ErrExecution
	}
	close(done)
	renewErr := <-renewed
	canceled := workCtx.Err() != nil
	stop()
	// A short callback or a process pause can finish before the ticker observes
	// expiry. Validate ownership once more before accepting any result.
	check, checkCancel := context.WithTimeout(context.Background(), 3*time.Second)
	finalErr := g.renewOnce(check, a.TaskID, lease.Token, owner)
	checkCancel()
	releaseErr := g.release(lease.Token, owner)
	if err != nil || renewErr != nil || finalErr != nil || canceled || ctx.Err() != nil || releaseErr != nil || len(out.Content) > 1<<20 || !utf8.ValidString(out.Content) || (out.Effect != runtime.NoEffect && out.Effect != runtime.ConfirmedEffect) {
		return runtime.ToolResult{Effect: runtime.UncertainEffect}, tools.ErrExecution
	}
	return out, nil
}

func dispatchWindow(ctx context.Context, now, approvalExpiry, leaseExpiry time.Time) bool {
	return ctx.Err() == nil && now.Before(approvalExpiry) && now.Before(leaseExpiry)
}

func reviewPrompt(ctx context.Context, fn tools.ApprovalReviewer, prompt tools.ApprovalPrompt) (actor string, allowed bool, err error) {
	defer func() {
		if recover() != nil {
			actor, allowed, err = "", false, tools.ErrDenied
		}
	}()
	return fn(ctx, prompt)
}

func review(ctx context.Context, fn func(context.Context, approvals.Request) (string, bool, error), req approvals.Request) (actor string, allowed bool, err error) {
	defer func() {
		if recover() != nil {
			actor, allowed, err = "", false, tools.ErrDenied
		}
	}()
	return fn(ctx, req)
}

func invoke(ctx context.Context, fn func(context.Context) (runtime.ToolResult, error)) (out runtime.ToolResult, err error) {
	defer func() {
		if recover() != nil {
			out, err = runtime.ToolResult{Effect: runtime.UncertainEffect}, tools.ErrExecution
		}
	}()
	return fn(ctx)
}

func (g *Gate) renew(ctx context.Context, cancel context.CancelFunc, done <-chan struct{}, task, token, owner string) error {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return tools.ErrExecution
		case <-ticker.C:
			heartbeat, stop := context.WithTimeout(ctx, 3*time.Second)
			err := g.renewOnce(heartbeat, task, token, owner)
			stop()
			if err != nil {
				cancel()
				return tools.ErrExecution
			}
		}
	}
}

func (g *Gate) renewOnce(ctx context.Context, task, token, owner string) error {
	canceled, err := g.Store.CancellationRequested(ctx, task)
	if err != nil || canceled {
		return tools.ErrExecution
	}
	return g.Store.RenewLease(ctx, token, owner, time.Now().UTC(), 30*time.Second)
}

func (g *Gate) release(token, owner string) error {
	cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return g.Store.ReleaseLease(cleanup, token, owner)
}
