package toolgate

import (
	"context"
	"time"

	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

func (g *Gate) oneReviewer() bool {
	count := 0
	if g.Review != nil {
		count++
	}
	if g.ReviewPrompt != nil {
		count++
	}
	if g.Present != nil {
		count++
	}
	return count == 1
}

func present(ctx context.Context, fn tools.ApprovalPresenter, prompt tools.ApprovalPrompt) (err error) {
	defer func() {
		if recover() != nil {
			err = tools.ErrDenied
		}
	}()
	return fn(ctx, prompt)
}

// awaitDecision observes durable state only. It does not manufacture a decision
// from presentation success or restore authority after consumption or revocation.
func (g *Gate) awaitDecision(ctx context.Context, req approvals.Request) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil || !time.Now().Before(req.ExpiresAt) {
			return tools.ErrDenied
		}
		status, err := g.Store.CancellationStatus(ctx, req.TaskID)
		if err != nil || status.Requested || status.State != "running" {
			return tools.ErrDenied
		}
		r, err := g.Store.ReadApproval(ctx, req.ID)
		if err != nil || r.Validate() != nil || !r.Request.Matches(req) {
			return tools.ErrDenied
		}
		switch r.State {
		case approvals.Approved:
			return nil
		case approvals.Pending:
		default:
			return tools.ErrDenied
		}
		select {
		case <-ctx.Done():
			return tools.ErrDenied
		case <-ticker.C:
		}
	}
}
