package remotecli

import (
	"context"
	"io"
	"math"
	"time"

	"github.com/ArronJablonowski/NexusRouter/remote"
)

func enqueueReviewOperation(ctx context.Context, client *remote.Client, automatic bool, queue, routes, root, key, config, reviewer string, cost float64, deadline time.Time, input io.Reader) (remote.ReviewJobStatus, error) {
	var zero remote.ReviewJobStatus
	if client == nil || config == "" || reviewer == "" || cost < 0 || math.IsNaN(cost) || math.IsInf(cost, 0) || deadline.IsZero() || deadline.After(time.Now().Add(24*time.Hour)) {
		return zero, remote.ErrInvalid
	}
	store, err := remote.OpenExistingRouteStore(routes)
	if err != nil {
		return zero, err
	}
	var task remote.Task
	if automatic {
		var request remote.AutomaticRequest
		if err = readTrustInput(input, &request); err != nil {
			return zero, err
		}
		task, _, err = store.ResolveAutomatic(key, request)
	} else {
		err = readTrustInput(input, &task)
	}
	if err != nil {
		return zero, err
	}
	if task.Validate() != nil {
		return zero, remote.ErrInvalid
	}
	policy, closeCoordinator, err := configuredReviewPolicy(ctx, config, reviewer, cost, task.Private)
	if err != nil {
		return zero, err
	}
	defer closeCoordinator()
	q, err := remote.OpenReviewQueue(queue)
	if err != nil {
		return zero, err
	}
	return client.EnqueueRecordedReview(ctx, q, store, root, key, task, policy, deadline)
}
func runReviewQueueOperation(ctx context.Context, client *remote.Client, queue, routes, root, config, reviewer string, cost float64, wait time.Duration) ([]remote.ReviewJobStatus, error) {
	if client == nil || config == "" || reviewer == "" || cost < 0 || math.IsNaN(cost) || math.IsInf(cost, 0) || wait <= 0 || wait > 24*time.Hour {
		return nil, remote.ErrInvalid
	}
	q, err := remote.OpenExistingReviewQueue(queue)
	if err != nil {
		return nil, err
	}
	store, err := remote.OpenExistingRouteStore(routes)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	policies := map[bool]remote.RemoteEvaluator{}
	closers := []func() error{}
	defer func() {
		for _, close := range closers {
			_ = close()
		}
	}()
	resolve := func(private bool) (remote.RemoteEvaluator, error) {
		if p, ok := policies[private]; ok {
			return p, nil
		}
		p, close, err := configuredReviewPolicy(ctx, config, reviewer, cost, private)
		if err == nil {
			policies[private] = p
			closers = append(closers, close)
		}
		return p, err
	}
	for {
		states, err := client.ProcessReviewJobs(ctx, q, store, root, resolve)
		if err != nil {
			return states, err
		}
		pending := false
		for _, s := range states {
			pending = pending || s.Status == "pending"
		}
		if !pending {
			return states, nil
		}
		timer := time.NewTimer(15 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return states, ctx.Err()
		case <-timer.C:
		}
	}
}
