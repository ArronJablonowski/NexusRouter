package sessions

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/runtime"
)

func interruptedWorkerFixture(t *testing.T) [][]runtime.Event {
	h := interruptedDelegationFixture(t)
	return [][]runtime.Event{h[1][:len(h[1])-1], h[2]}
}

func TestPlanInterruptedWorkerFailsWithoutAcceptingOutput(t *testing.T) {
	for _, prefix := range []int{2, 3, 4} {
		h := interruptedWorkerFixture(t)
		h[0] = h[0][:prefix]
		before, _ := json.Marshal(h)
		now := time.Now().UTC()
		plan, err := PlanInterruptedWorker(h, now)
		if err != nil || plan.ParentTaskID != "work" || plan.ExpectedSequence != int64(prefix) || len(plan.Events) != 1 {
			t.Fatal(plan, err)
		}
		e := plan.Events[0]
		if e.Kind != runtime.TaskFailed || e.Data.Code != "worker_owner_interrupted" || e.Data.Text != "" || e.Data.Accepted != nil || e.WorkerID != "worker" || e.CausationID != h[0][prefix-1].ID {
			t.Fatal("incorrect terminal", e)
		}
		again, err := PlanInterruptedWorker([][]runtime.Event{h[1], h[0]}, now)
		if err != nil || !reflect.DeepEqual(plan, again) {
			t.Fatal("nondeterministic plan", again, err)
		}
		after, _ := json.Marshal(h)
		if !bytes.Equal(before, after) {
			t.Fatal("modified source history")
		}
		full := append(append([]runtime.Event{}, h[0]...), e)
		payload, err := recoveryWorkResult(full, h[1])
		if err != nil || bytes.Contains(payload, []byte("untrusted_output")) || !bytes.Contains(payload, []byte("worker_owner_interrupted")) {
			t.Fatal("recovered failure incompatible with parent recovery", string(payload), err)
		}
	}
}

func TestPlanInterruptedWorkerRejectsUnsupportedHistory(t *testing.T) {
	for _, mode := range []string{"missing child", "extra child", "running child", "wrong parent", "wrong submission", "missing origin", "wrong worker", "wrong correlation", "completed worker", "missing started", "out of order", "future", "effect", "confirmed effect", "worker effect", "hidden worker data", "unknown child session", "zero time", "utc overflow", "nested child"} {
		t.Run(mode, func(t *testing.T) {
			h := interruptedWorkerFixture(t)
			now := time.Now().UTC()
			switch mode {
			case "missing child":
				h = h[:1]
			case "extra child", "nested child":
				h = append(h, h[1])
			case "running child":
				h[1] = h[1][:len(h[1])-1]
			case "wrong parent":
				h[1][0].Data.ParentTaskID = "elsewhere"
			case "wrong submission":
				h[1][0].Data.SubmissionID = "elsewhere"
			case "missing origin":
				h[0][0].Data.DelegationOrigin = nil
			case "wrong worker":
				h[0][1].WorkerID = "other"
			case "wrong correlation":
				h[0][1].CorrelationID = "other"
			case "completed worker":
				h[0][3].Kind = runtime.TaskCompleted
			case "missing started":
				h[0][1].Kind = runtime.WorkerHeartbeat
			case "out of order":
				h[0][2], h[0][3] = h[0][3], h[0][2]
			case "future":
				h[1][0].Time = now.Add(time.Hour)
			case "effect":
				h[1][0].Data.Effect = runtime.UncertainEffect
			case "confirmed effect":
				h[1][0].Data.Effect = runtime.ConfirmedEffect
			case "worker effect":
				h[0][0].Data.Effect = runtime.NoEffect
			case "hidden worker data":
				h[0][1].Data.Code = "unrecognized"
			case "unknown child session":
				h[1][0].SessionID = ""
			case "zero time":
				now = time.Time{}
			case "utc overflow":
				now = time.Date(2260, 12, 31, 23, 30, 0, 0, time.FixedZone("negative", -3600))
			}
			plan, err := PlanInterruptedWorker(h, now)
			if !errors.Is(err, ErrHistory) || len(plan.Events) != 0 {
				t.Fatal("unsafe plan", plan, err)
			}
		})
	}
}

func TestPlanInterruptedWorkerUnsubmitted(t *testing.T) {
	h := interruptedWorkerFixture(t)
	h[0][0].Data.SubmissionID = ""
	h[1][0].Data.SubmissionID = ""
	before, _ := json.Marshal(h)
	plan, err := PlanInterruptedWorker(h, time.Now().UTC())
	if err != nil || len(plan.Events) != 1 || plan.Events[0].Data.SubmissionID != "" {
		t.Fatal(plan, err)
	}
	after, _ := json.Marshal(h)
	if !bytes.Equal(before, after) {
		t.Fatal("private validation changed source")
	}
}
