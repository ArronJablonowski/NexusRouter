package telemetry

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func TestOutputValidityUsesFinalSteeredAttempt(t *testing.T) {
	for _, mode := range []string{"valid", "duplicate", "exhausted", "invalidfinal"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := submissionStore(t)
			ctx := context.Background()
			first := validityEvents("task", true)
			appendValidity(t, db, first[:4])
			steering, err := db.QueueSteering(ctx, "task", submitDigest("steering"), "new instruction")
			if err != nil {
				t.Fatal(err)
			}
			applied := event("steering", 5, runtime.SteeringApplied)
			applied.Data.SteeringID = steering.ID
			applied.Data.Text = steering.Text
			if err = db.Append(ctx, 4, applied); err != nil {
				t.Fatal(err)
			}
			if mode == "exhausted" {
				failed := event("limit", 6, runtime.TaskFailed)
				failed.AttemptID = "attempt"
				failed.TurnID = "turn"
				failed.Data.Code = "turn_limit"
				if err = db.Append(ctx, 5, failed); err != nil {
					t.Fatal(err)
				}
			} else {
				final := validityEvents("task", true)[1:]
				for i := range final {
					final[i].ID = fmt.Sprintf("final-%d", i)
					final[i].Sequence = int64(i + 6)
					final[i].AttemptID = "second"
					final[i].TurnID = "secondturn"
				}
				if mode == "invalidfinal" {
					wrong := false
					final[2].Data.Accepted = &wrong
				}
				if mode == "duplicate" {
					duplicate := final[2]
					duplicate.ID = "duplicate"
					duplicate.Sequence = 9
					final[3].Sequence = 10
					final = append(final[:3], duplicate, final[3])
				}
				for _, e := range final {
					if err = db.Append(ctx, e.Sequence-1, e); err != nil {
						t.Fatal(err)
					}
				}
			}
			out, err := db.OutputValidity(ctx, validityKey())
			switch mode {
			case "valid":
				if err != nil || out.Samples != 1 || out.Failures != 0 {
					t.Fatal(out, err)
				}
			case "exhausted":
				if err != nil || out.Samples != 0 {
					t.Fatal("stale answer counted", out, err)
				}
			default:
				if !errors.Is(err, evaluation.ErrEvidence) {
					t.Fatal(out, err)
				}
			}
		})
	}
}
