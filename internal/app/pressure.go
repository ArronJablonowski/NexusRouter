package app

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/ArronJablonowski/NexusRouter/resources"
)

// ErrPressureTimeout is an admission failure, not a request to cancel a task.
var ErrPressureTimeout = errors.New("resource pressure admission timed out")

// runWithPressure retries only pre-task resource denials. The queue deadline is
// carried separately from execution: admitted work keeps the caller's context.
func (s *Service) runWithPressure(ctx context.Context, r Request, execute func(context.Context, Request) (Result, error)) (Result, error) {
	// Freeze one execution identity across pressure retries. A denied attempt
	// never becomes durable, while an admitted retry binds its resource claim
	// and TaskStarted event to the same identity.
	if r.runtimeHostAdmission == nil && r.taskID == "" {
		r.taskID = rand.Text()
	}
	if r.runtimeHostAdmission == nil && r.sessionID == "" && r.ContinueTaskID == "" {
		r.sessionID = r.taskID
	}
	if s.settings.Hardware.LocalPressurePolicy != "wait" {
		return execute(ctx, r)
	}
	duration, err := time.ParseDuration(s.settings.Hardware.LocalQueueTimeout)
	if err != nil || duration < 100*time.Millisecond || duration > 5*time.Minute {
		return Result{}, ErrAdmission
	}
	queue, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	interval := min(250*time.Millisecond, duration/4)
	r.admissionContext = queue
	stopped := func() (Result, error) {
		cause := ctx.Err()
		if cause == nil {
			cause = ErrPressureTimeout
		}
		return Result{}, errors.Join(ErrAdmission, resources.ErrCapacity, cause)
	}
	for {
		if queue.Err() != nil {
			return stopped()
		}
		result, err := execute(ctx, r)
		if result.TaskID != "" || err == nil {
			return result, err
		}
		if queue.Err() != nil {
			return stopped()
		}
		if !errors.Is(err, resources.ErrCapacity) {
			return result, err
		}
		timer := time.NewTimer(interval)
		select {
		case <-queue.Done():
			timer.Stop()
			return stopped()
		case <-timer.C:
		}
	}
}
