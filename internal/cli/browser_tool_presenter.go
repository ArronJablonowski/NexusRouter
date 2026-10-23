package cli

import (
	"context"
	"crypto/rand"
	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

// This presenter only acknowledges that a supported proposal can be shown.
// Manual mode waits for a browser decision. Explicit auto mode records a
// durable decision; the gate still consumes it under the existing lease.
func browserToolPresenter(s config.Settings) tools.ApprovalPresenter {
	return func(ctx context.Context, p tools.ApprovalPrompt) error {
		if ctx == nil || ctx.Err() != nil || !s.WebUI.Enabled {
			return tools.ErrDenied
		}
		if s.Tools.AutoApprove {
			db, err := telemetry.OpenApprovalControl(ctx, s.Telemetry.Database)
			if err != nil {
				return tools.ErrDenied
			}
			defer db.Close()
			current, err := db.ReadApproval(ctx, p.Request.ID)
			if err != nil || !current.Request.Matches(p.Request) || current.State != approvals.Pending {
				return tools.ErrDenied
			}
			_, err = db.DecideBoundApproval(ctx, approvals.Command{Expected: p.Request, ID: rand.Text(), Allowed: true}, "auto_approval_setting", time.Now().UTC())
			return err
		}
		if p.Request.ToolName == "create_file" && s.Tools.Enabled && s.Tools.CreateEnabled {
			return nil
		}
		if (p.Request.ToolName == "comfy_submit" || p.Request.ToolName == "comfy_cancel_pending") && s.ComfyUI.Enabled {
			return nil
		}
		return tools.ErrDenied
	}
}
