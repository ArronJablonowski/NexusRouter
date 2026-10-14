package cli

import (
	"context"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

// This presenter only acknowledges that a supported proposal can be shown.
// Execution still waits for the existing authenticated durable browser decision.
func browserToolPresenter(s config.Settings) tools.ApprovalPresenter {
	return func(ctx context.Context, p tools.ApprovalPrompt) error {
		if ctx == nil || ctx.Err() != nil || !s.WebUI.Enabled {
			return tools.ErrDenied
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
