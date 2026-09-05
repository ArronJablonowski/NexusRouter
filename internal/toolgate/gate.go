// Package toolgate binds operator approvals to durable, single-use tool work.
package toolgate

import (
	"context"
	"crypto/rand"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

// Gate is configured by trusted host code and must remain immutable during use.
// Review authenticates its returned actor; a name alone is not authentication.
// Review and handlers are cooperative in-process callbacks, not sandboxed code.
// They must honor cancellation, and handlers must join any work they start
// before returning. A canceled callback is never abandoned or retried.
type Gate struct {
	Store  *telemetry.Store
	Review func(context.Context, approvals.Request) (actor string, allowed bool, err error)
}

var _ tools.Authority = (*Gate)(nil)

// ExecuteApproved persists review and consumes authority before invoking handler.
// It deliberately cannot resume an earlier approval: duplicate calls fail closed.
func (g *Gate) ExecuteApproved(ctx context.Context, a tools.Authorization, handler func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
	noEffect := runtime.ToolResult{Effect: runtime.NoEffect}
	if g == nil || g.Store == nil || g.Review == nil || handler == nil || ctx.Err() != nil {
		return noEffect, tools.ErrDenied
	}
	now := time.Now().UTC()
	req := approvals.Request{Version: approvals.Version, ID: rand.Text(), TaskID: a.TaskID, TurnID: a.TurnID, ToolCallID: a.ToolCallID, ToolName: a.ToolName, Scope: a.Scope, ArgumentsDigest: a.ArgumentsDigest, SchemaDigest: a.SchemaDigest, PolicyDigest: a.PolicyDigest, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if req.Validate() != nil {
		return noEffect, tools.ErrDenied
	}
	snapshot, err := g.Store.TaskSnapshot(ctx, a.TaskID)
	pending := snapshot.Pending[a.ToolCallID]
	if err != nil || snapshot.SessionID != a.SessionID || pending.AttemptID != a.AttemptID || a.AttemptID == "" || a.SessionID == "" {
		return noEffect, tools.ErrDenied
	}
	if _, err = g.Store.RequestApproval(ctx, req); err != nil {
		return noEffect, tools.ErrDenied
	}
	reviewCtx, cancel := context.WithDeadline(ctx, req.ExpiresAt)
	actor, allowed, err := review(reviewCtx, g.Review, req)
	late := reviewCtx.Err() != nil
	cancel()
	if err != nil || late {
		return noEffect, tools.ErrDenied
	}
	decision := approvals.Decision{ID: rand.Text(), Actor: actor, Allowed: allowed, Time: time.Now().UTC()}
	if _, err = g.Store.DecideApproval(ctx, req.ID, decision); err != nil || !allowed {
		return noEffect, tools.ErrDenied
	}
	owner := rand.Text()
	lease, err := g.Store.AcquireLease(ctx, a.TaskID, owner, a.Scope, true, time.Now().UTC(), 30*time.Second)
	if err != nil {
		return noEffect, tools.ErrDenied
	}
	// Even an ambiguous consumption acknowledgement cannot authorize a retry.
	if _, err = g.Store.ConsumeApproval(ctx, req, lease.Token, owner, time.Now().UTC()); err != nil {
		g.release(lease.Token, owner)
		return runtime.ToolResult{Effect: runtime.UncertainEffect}, tools.ErrExecution
	}
	workCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	renewed := make(chan error, 1)
	go func() { renewed <- g.renew(workCtx, stop, done, a.TaskID, lease.Token, owner) }()
	var out runtime.ToolResult
	if workCtx.Err() == nil {
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
