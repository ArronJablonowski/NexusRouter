package submissions

import (
	"testing"
	"time"
)

func TestInterruptedDelegationRecoveryReceipt(t *testing.T) {
	for _, action := range []string{"failed", "canceled", "queued", "running", "succeeded"} {
		r := Recovery{Version: 1, ID: "receipt", SubmissionID: "submission", Time: time.Now(), Action: action, Reason: "interrupted_delegation"}
		if valid := r.Validate() == nil; valid != (action == "failed" || action == "canceled") {
			t.Fatalf("action %s accepted=%v", action, valid)
		}
	}
}
