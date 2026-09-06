package workers

import (
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

var ErrTaskLeaseStatus = errors.New("task lease status unavailable")

// TaskLeaseStatus is a bounded observation, not admission or process-liveness proof.
// Nil feature groups mean unsupported storage, never a verified zero count.
type TaskLeaseStatus struct {
	Version       int                 `json:"version"`
	TaskID        string              `json:"task_id"`
	TaskState     string              `json:"task_state"`
	Sequence      int64               `json:"sequence"`
	ObservedAt    time.Time           `json:"observed_at"`
	StorageSchema int                 `json:"storage_schema"`
	Leases        *TaskLeaseCounts    `json:"leases"`
	Recoveries    *TaskRecoveryCounts `json:"recoveries"`
}

type TaskLeaseCounts struct {
	LiveReaders     int64 `json:"live_readers"`
	ExpiredReaders  int64 `json:"expired_readers"`
	LiveWriters     int64 `json:"live_writers"`
	ExpiredWriters  int64 `json:"expired_writers"`
	ReleasedReaders int64 `json:"released_readers"`
	ReleasedWriters int64 `json:"released_writers"`
}

type TaskRecoveryCounts struct {
	TerminalReaders     int64 `json:"terminal_readers"`
	OrphanWorkers       int64 `json:"orphan_workers"`
	InterruptedChildren int64 `json:"interrupted_children"`
}

func (s TaskLeaseStatus) Validate() error {
	_, offset := s.ObservedAt.Zone()
	if s.Version != 1 || !sessions.ValidEventPageID(s.TaskID) || s.Sequence < 1 || s.Sequence > 10000 || offset != 0 || s.ObservedAt.Year() < 1970 || s.ObservedAt.Year() >= 2261 || s.StorageSchema < 1 || s.StorageSchema > 24 || (s.Leases == nil) != (s.StorageSchema < 3) || (s.Recoveries == nil) != (s.StorageSchema < 23) {
		return ErrTaskLeaseStatus
	}
	switch s.TaskState {
	case "running", "completed", "failed", "canceled":
	default:
		return ErrTaskLeaseStatus
	}
	if s.TaskState != "running" && s.Sequence < 2 {
		return ErrTaskLeaseStatus
	}
	if s.Leases != nil {
		l := s.Leases
		var total int64
		for _, n := range []int64{l.LiveReaders, l.ExpiredReaders, l.LiveWriters, l.ExpiredWriters, l.ReleasedReaders, l.ReleasedWriters} {
			if n < 0 || n > 1000 {
				return ErrTaskLeaseStatus
			}
			total += n
		}
		if total > 1000 {
			return ErrTaskLeaseStatus
		}
	}
	if r := s.Recoveries; r != nil {
		if r.TerminalReaders < 0 || r.TerminalReaders > 1000 || r.OrphanWorkers < 0 || r.OrphanWorkers > 1000 || r.InterruptedChildren < 0 || r.InterruptedChildren > r.OrphanWorkers || r.TerminalReaders+r.OrphanWorkers > s.Leases.ReleasedReaders {
			return ErrTaskLeaseStatus
		}
		if s.TaskState == "running" && (r.TerminalReaders != 0 || r.OrphanWorkers != 0) || r.OrphanWorkers > 0 && s.TaskState != "failed" {
			return ErrTaskLeaseStatus
		}
	}
	return nil
}
