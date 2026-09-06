package workers

import (
	"math"
	"testing"
	"time"
)

func TestTaskLeaseStatusValidation(t *testing.T) {
	base := TaskLeaseStatus{Version: 1, TaskID: "task", TaskState: "failed", Sequence: 2, ObservedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), StorageSchema: 23, Leases: &TaskLeaseCounts{ReleasedReaders: 2}, Recoveries: &TaskRecoveryCounts{TerminalReaders: 1, OrphanWorkers: 1, InterruptedChildren: 1}}
	if base.Validate() != nil {
		t.Fatal("valid rejected")
	}
	for _, mutate := range []func(*TaskLeaseStatus){
		func(s *TaskLeaseStatus) { s.Sequence = 1 },
		func(s *TaskLeaseStatus) { s.TaskState = "running" }, func(s *TaskLeaseStatus) { s.TaskState = "completed" }, func(s *TaskLeaseStatus) { s.TaskState = "canceled" },
		func(s *TaskLeaseStatus) { s.Version = 2 }, func(s *TaskLeaseStatus) { s.TaskID = "bad\n" }, func(s *TaskLeaseStatus) { s.TaskState = "unknown" }, func(s *TaskLeaseStatus) { s.Sequence = 10001 }, func(s *TaskLeaseStatus) { s.StorageSchema = 28 }, func(s *TaskLeaseStatus) { s.ObservedAt = time.Time{} }, func(s *TaskLeaseStatus) { s.Leases = nil }, func(s *TaskLeaseStatus) { s.Recoveries = nil }, func(s *TaskLeaseStatus) { s.Leases.LiveReaders = math.MaxInt64 }, func(s *TaskLeaseStatus) { s.Leases.LiveReaders = -1 }, func(s *TaskLeaseStatus) { s.Leases.LiveReaders = 1000 }, func(s *TaskLeaseStatus) { s.Recoveries.InterruptedChildren = 2 }, func(s *TaskLeaseStatus) { s.Recoveries.OrphanWorkers = 3 },
	} {
		s := base
		l := *base.Leases
		r := *base.Recoveries
		s.Leases = &l
		s.Recoveries = &r
		mutate(&s)
		if s.Validate() == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
	for schema := 1; schema <= 27; schema++ {
		s := base
		s.StorageSchema = schema
		if schema < 23 {
			s.Recoveries = nil
		}
		if schema < 3 {
			s.Leases = nil
		}
		if s.Validate() != nil {
			t.Fatal(schema)
		}
	}
}
