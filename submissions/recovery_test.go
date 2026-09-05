package submissions

import (
	"testing"
	"time"
)

func TestInterruptedDelegationRecoveryReceipt(t *testing.T) {
	for _, reason := range []string{"interrupted_delegation", "interrupted_model"} {
		for _, action := range []string{"failed", "canceled", "queued", "running", "succeeded"} {
			r := Recovery{Version: 1, ID: "receipt", SubmissionID: "submission", Time: time.Now(), Action: action, Reason: reason}
			if valid := r.Validate() == nil; valid != (action == "failed" || action == "canceled") {
				t.Fatalf("reason %s action %s accepted=%v", reason, action, valid)
			}
		}
	}
}
