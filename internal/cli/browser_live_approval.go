package cli

import (
	"context"
	"sync"
	"time"

	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

// The mutex makes disabling linearize after any in-flight durable decision.
// Only successfully persisted Settings updates may change this controller.
type browserLiveApproval struct {
	mu       sync.Mutex
	settings config.Settings
}

func (a *browserLiveApproval) set(enabled bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.settings.Tools.AutoApprove = enabled
}

func (a *browserLiveApproval) present(ctx context.Context, p tools.ApprovalPrompt) error {
	if ctx == nil || ctx.Err() != nil {
		return tools.ErrDenied
	}
	a.mu.Lock()
	settings := a.settings
	a.mu.Unlock()
	if !settings.WebUI.Enabled {
		return tools.ErrDenied
	}
	// Opening validates database integrity. Do this once per proposal, never
	// once per poll: repeated quick_check scans starve large live stores.
	db, err := telemetry.OpenApprovalControl(ctx, settings.Telemetry.Database)
	if err != nil {
		return tools.ErrDenied
	}
	defer db.Close()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		a.mu.Lock()
		s := a.settings
		if !s.Tools.AutoApprove && browserToolPresenter(s)(ctx, p) != nil {
			a.mu.Unlock()
			return tools.ErrDenied
		}
		current, err := db.ReadApproval(ctx, p.Request.ID)
		status, statusErr := db.CancellationStatus(ctx, p.Request.TaskID)
		if err != nil || statusErr != nil || status.Requested || status.State != "running" || !time.Now().Before(p.Request.ExpiresAt) || !current.Request.Matches(p.Request) {
			a.mu.Unlock()
			return tools.ErrDenied
		}
		if current.State != approvals.Pending {
			a.mu.Unlock()
			return nil
		}
		if s.Tools.AutoApprove {
			err = browserToolPresenter(s)(ctx, p)
			a.mu.Unlock()
			return err
		}
		a.mu.Unlock()
		// The normal gate still checks cancellation, expiry and consumption.
		select {
		case <-ctx.Done():
			return tools.ErrDenied
		case <-ticker.C:
		}
	}
}
