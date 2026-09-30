package telemetry

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/submissions"
)

func TestTerminalTreeAggregateSourceByteBudget(t *testing.T) {
	for _, size := range []int{3 << 20, 4 << 20} {
		t.Run(fmt.Sprintf("%dMiB", size>>20), func(t *testing.T) {
			db, _ := submissionStore(t)
			ctx := context.Background()
			job := queuedSubmission(t, db, "aggregate")
			claim := claimSubmission(t, db)
			terminalFixture(t, db, claim, "first", "", "failed")
			terminalFixture(t, db, claim, "second", "first", "success")
			// Both individual histories fit the old per-task limit. Padding is
			// deliberately outside the decoded schema to test raw-source preflight,
			// rather than relying on a later canonical JSON-size check.
			if _, err := db.db.ExecContext(ctx, `UPDATE events SET body=json_set(body,'$.fixture_padding',printf('%.*c',?,'x')) WHERE sequence=1`, size); err != nil {
				t.Fatal(err)
			}
			recovered, err := db.RecoverTerminalSubmission(ctx, job.ID, submitDigest("config"), time.Now().Add(2*time.Minute))
			if size == 3<<20 {
				if err != nil || !recovered {
					t.Fatal(recovered, err)
				}
			} else {
				if recovered || !errors.Is(err, submissions.ErrInvalid) {
					t.Fatal(recovered, err)
				}
				status, err := db.Submission(ctx, job.ID)
				if err != nil || status.State != "running" || status.Result != nil {
					t.Fatal(status, err)
				}
				audits, err := db.RecoveryHistory(ctx, job.ID)
				if err != nil || len(audits) != 0 {
					t.Fatal(audits, err)
				}
			}
		})
	}
}

func TestTerminalTreeOriginalContinuationBinding(t *testing.T) {
	for _, mode := range []string{"matching", "mismatch", "non_string"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := submissionStore(t)
			ctx := context.Background()
			request := []byte(`{"request":{"ContinueTaskID":"prior-task"}}`)
			if mode == "mismatch" {
				request = []byte(`{"request":{"ContinueTaskID":"different-task"}}`)
			}
			if mode == "non_string" {
				request = []byte(`{"request":{"ContinueTaskID":123}}`)
			}
			job := queuedSubmissionBody(t, db, "continuation", request)
			claim := claimSubmission(t, db)
			terminalFixture(t, db, claim, "task", "", "success")
			if _, err := db.db.ExecContext(ctx, `UPDATE events SET body=json_set(body,'$.data.parent_task_id','prior-task') WHERE sequence=1`); err != nil {
				t.Fatal(err)
			}
			recovered, err := db.RecoverTerminalSubmission(ctx, job.ID, submitDigest("config"), time.Now().Add(2*time.Minute))
			if mode == "matching" {
				if err != nil || !recovered {
					t.Fatal(recovered, err)
				}
			} else if recovered || !errors.Is(err, submissions.ErrInvalid) {
				t.Fatal(recovered, err)
			}
		})
	}
}
