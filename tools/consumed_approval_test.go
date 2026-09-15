package tools

import (
	"context"
	"testing"
)

func TestConsumedApprovalContextIsBoundedProvenance(t *testing.T) {
	ctx := context.Background()
	if _, ok := ConsumedApprovalFromContext(ctx); ok {
		t.Fatal("empty context exposed approval")
	}
	ctx = WithConsumedApproval(ctx, ConsumedApproval{ID: "approval_1"})
	if got, ok := ConsumedApprovalFromContext(ctx); !ok || got.ID != "approval_1" {
		t.Fatal(got, ok)
	}
	for _, invalid := range []string{"", "approval 1", "approval/1"} {
		if _, ok := ConsumedApprovalFromContext(WithConsumedApproval(context.Background(), ConsumedApproval{ID: invalid})); ok {
			t.Fatal("invalid approval admitted", invalid)
		}
	}
}
