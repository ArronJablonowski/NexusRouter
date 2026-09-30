package cli

import (
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/tools"
)

func TestCreatePreviewChecksBehaviorDeclaration(t *testing.T) {
	cfg, prompt := chatApprovalFixture(t, "fixed fixture")
	prompt.Request.ToolBehavior = tools.BehaviorNonIdempotentWrite
	preview, err := chatApprovalPreview(cfg, nil, prompt)
	if err != nil || !strings.Contains(preview, "Declared behavior: non_idempotent_write (not retry authority)") {
		t.Fatal("missing declared behavior", err)
	}
	for _, behavior := range []tools.Behavior{tools.BehaviorReadOnly, tools.BehaviorIdempotentWrite, "unknown"} {
		prompt.Request.ToolBehavior = behavior
		if _, err := chatApprovalPreview(cfg, nil, prompt); err == nil {
			t.Fatal("incorrect create declaration accepted")
		}
	}
}
