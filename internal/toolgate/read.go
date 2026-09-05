package toolgate

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/runtime"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

var _ tools.ReadAuthority = (*Gate)(nil)

// ExecuteRead needs no approval reviewer: policy and schema admission happened
// in the executor. It binds the pending durable read to a shared exact-scope
// lease, joins cooperative work, and discards results after ownership loss.
func (g *Gate) ExecuteRead(ctx context.Context, x runtime.ToolExecution, scope string, handler func(context.Context) (runtime.ToolResult, error)) (runtime.ToolResult, error) {
	noEffect := runtime.ToolResult{Effect: runtime.NoEffect}
	if ctx == nil || ctx.Err() != nil || g == nil || g.Store == nil || handler == nil || len(scope) == 0 || len(scope) > 256 || !utf8.ValidString(scope) || strings.Contains(scope, "*") || strings.IndexFunc(scope, unicode.IsControl) >= 0 || !g.pendingRead(ctx, x) {
		return noEffect, tools.ErrDenied
	}
	owner := rand.Text()
	lease, err := g.Store.AcquireLease(ctx, x.TaskID, owner, scope, false, time.Now().UTC(), 30*time.Second)
	if err != nil {
		return noEffect, tools.ErrDenied
	}
	workCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	renewed := make(chan error, 1)
	go func() { renewed <- g.renewRead(workCtx, stop, done, x.TaskID, lease.Token, owner) }()
	var out runtime.ToolResult
	if workCtx.Err() == nil && time.Now().UTC().Before(lease.Expires) && g.pendingRead(workCtx, x) {
		out, err = invoke(workCtx, handler)
	} else {
		err = tools.ErrExecution
	}
	close(done)
	renewErr := <-renewed
	canceled := workCtx.Err() != nil
	stop()
	check, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	// Cancellation is not ownership loss: verify the lease independently before
	// retaining any known effect-free failure diagnostic from joined work.
	finalErr := g.Store.RenewLease(check, lease.Token, owner, time.Now().UTC(), 30*time.Second)
	durableCanceled, cancelErr := g.Store.CancellationRequested(check, x.TaskID)
	cancel()
	releaseErr := g.release(lease.Token, owner)
	valid := out.Effect == runtime.NoEffect && (!out.Recoverable || out.Failed) && len(out.Content) <= 1<<20 && utf8.ValidString(out.Content)
	if ctx.Err() != nil && err == nil && finalErr == nil && cancelErr == nil && releaseErr == nil && (renewErr == nil || errors.Is(renewErr, context.Canceled) || errors.Is(renewErr, context.DeadlineExceeded)) && valid && out.Failed {
		out.Recoverable = false
		return out, ctx.Err()
	}
	if err != nil || renewErr != nil || finalErr != nil || cancelErr != nil || releaseErr != nil || durableCanceled || canceled || ctx.Err() != nil || !valid {
		return runtime.ToolResult{Effect: runtime.UncertainEffect}, tools.ErrExecution
	}
	return out, nil
}

// Unlike the approval gate's generic failure, distinguish observed cancellation
// from renewal/storage failure. Only the former can retain a failed diagnostic.
func (g *Gate) renewRead(ctx context.Context, cancel context.CancelFunc, done <-chan struct{}, task, token, owner string) error {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			heartbeat, stop := context.WithTimeout(ctx, 3*time.Second)
			canceled, err := g.Store.CancellationRequested(heartbeat, task)
			if err == nil && !canceled {
				err = g.Store.RenewLease(heartbeat, token, owner, time.Now().UTC(), 30*time.Second)
			}
			stop()
			if err != nil || canceled {
				callerErr := ctx.Err()
				cancel()
				return readRenewalFailure(err, callerErr, canceled)
			}
		}
	}
}

func readRenewalFailure(storageErr, callerErr error, durableCanceled bool) error {
	// A coincident caller cancellation cannot turn a real storage failure into
	// permission to retain a diagnostic, even if the final ownership check works.
	if storageErr != nil && !errors.Is(storageErr, context.Canceled) && !errors.Is(storageErr, context.DeadlineExceeded) {
		return tools.ErrExecution
	}
	if callerErr != nil {
		return callerErr
	}
	if storageErr == nil && durableCanceled {
		return context.Canceled
	}
	return tools.ErrExecution
}

func (g *Gate) pendingRead(ctx context.Context, x runtime.ToolExecution) bool {
	if x.TaskID == "" || x.SessionID == "" || x.TurnID == "" || x.AttemptID == "" || x.Call.ID == "" || x.Call.Name == "" {
		return false
	}
	snapshot, err := g.Store.TaskSnapshot(ctx, x.TaskID)
	pending, ok := snapshot.Pending[x.Call.ID]
	if err != nil || snapshot.State != "running" || snapshot.SessionID != x.SessionID || !ok || !pending.Dispatched || pending.TurnID != x.TurnID || pending.AttemptID != x.AttemptID || pending.Call.Name != x.Call.Name || pending.ToolBehavior != runtime.BehaviorReadOnly {
		return false
	}
	canceled, err := g.Store.CancellationRequested(ctx, x.TaskID)
	return err == nil && !canceled
}
