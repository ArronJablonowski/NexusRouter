package cli

import (
	"context"
	"sync/atomic"

	"github.com/ArronJablonowski/NexusRouter/health"
)

// This state describes worker liveness, not the verdict of individual reviews.
// Terminal job receipts and current review heads remain authoritative for those.
type remoteReviewSupervisor struct {
	cancel context.CancelFunc
	done   chan struct{}
	state  atomic.Int32 // 0 running, 1 stopped, 2 failed
}

func startRemoteReviewSupervisor(ctx context.Context, run func(context.Context) error) *remoteReviewSupervisor {
	ctx, cancel := context.WithCancel(ctx)
	s := &remoteReviewSupervisor{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		_ = run(ctx)
		if ctx.Err() == nil { // Unexpected nil return is also a stopped worker.
			s.state.Store(2)
		} else {
			s.state.Store(1)
		}
	}()
	return s
}
func (s *remoteReviewSupervisor) Close() {
	if s != nil {
		s.cancel()
		<-s.done
	}
}
func (s *remoteReviewSupervisor) ready() bool { return s == nil || s.state.Load() == 0 }
func (s *remoteReviewSupervisor) health() health.Check {
	check := health.Check{Component: "remote_review", Status: "disabled", Code: "disabled_by_policy"}
	if s == nil {
		return check
	}
	switch s.state.Load() {
	case 0:
		check.Status, check.Code = "healthy", "supervisor_ok"
	case 1:
		check.Status, check.Code = "unavailable", "supervisor_stopped"
	default:
		check.Status, check.Code = "degraded", "supervisor_error"
	}
	return check
}
