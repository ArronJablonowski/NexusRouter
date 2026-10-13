package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/ArronJablonowski/NexusRouter/approvals"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	contract "github.com/ArronJablonowski/NexusRouter/webui"
)

func (b *BrowserMutations) comfyApprovalProposal(ctx context.Context, record approvals.Record) (*contract.ApprovalProposal, error) {
	snapshot, e := InspectTask(ctx, b.service.settings.Telemetry.Database, record.Request.TaskID)
	if e != nil {
		return nil, e
	}
	pending, ok := snapshot.Pending[record.Request.ToolCallID]
	if !ok || !pending.Dispatched || pending.ToolBehavior != runtime.BehaviorNonIdempotentWrite || record.Request.ToolBehavior != runtime.BehaviorNonIdempotentWrite || pending.TurnID != record.Request.TurnID || pending.Call.Name != record.Request.ToolName || len(pending.Call.Arguments) > 64<<10 {
		return nil, ErrBrowserMutation
	}
	d := sha256.Sum256(pending.Call.Arguments)
	if hex.EncodeToString(d[:]) != record.Request.ArgumentsDigest || !selectionValueClean(pending.Call.Arguments, memorySecrets(b.service.settings, b.service.secret)) {
		return nil, ErrBrowserMutation
	}
	kind := "comfy_workflow"
	if record.Request.ToolName == "comfy_cancel_pending" {
		kind = "comfy_cancel"
	}
	p := &contract.ApprovalProposal{Version: 1, Kind: kind, Arguments: pending.Call.Arguments}
	if p.Validate() != nil {
		return nil, ErrBrowserMutation
	}
	return p, nil
}
