package webui

import "testing"

func TestClaimAttentionIsEventOnly(t *testing.T) {
	revision := int64(1)
	request := BoardRequest{Version: ContractVersion, Action: ClaimAttention, IdempotencyKey: "claim-attention-key", BoardID: "board-a",
		CardID: "card-a", AttemptID: "attempt-a", ClaimID: "claim-a", ExpectedClaimRevision: &revision}
	if request.Validate() == nil {
		t.Fatal("supervisor observation admitted as a public mutation")
	}
}
