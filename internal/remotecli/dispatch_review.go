package remotecli

import (
	"context"
	"io"
	"math"
	"path/filepath"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

type dispatchReviewResult struct {
	Version    int                            `json:"version"`
	Phase      string                         `json:"phase"`
	Dispatch   submissions.Status             `json:"dispatch"`
	Choice     *remote.AutomaticChoice        `json:"choice,omitempty"`
	Evaluation *remote.RemoteEvaluationStatus `json:"evaluation,omitempty"`
}

func dispatchReviewOperation(ctx context.Context, client *remote.Client, automatic bool, destination, routes, root, key, configFile, reviewer string, maxCost float64, wait time.Duration, input io.Reader) (dispatchReviewResult, error) {
	result := dispatchReviewResult{Version: 1, Phase: "not_dispatched"}
	if wait <= 0 || wait > 24*time.Hour || configFile == "" || reviewer == "" || maxCost < 0 || math.IsNaN(maxCost) || math.IsInf(maxCost, 0) || !filepath.IsAbs(root) || filepath.Clean(root) != root || automatic && destination != "" {
		return result, remote.ErrInvalid
	}
	var task remote.Task
	var request remote.AutomaticRequest
	private := false
	if automatic {
		if err := readTrustInput(input, &request); err != nil {
			return result, err
		}
		if request.Version != 1 || request.Routing.AllowExploration {
			return result, remote.ErrInvalid
		}
		private = request.Routing.LocalRequired
	} else {
		if err := readTrustInput(input, &task); err != nil {
			return result, err
		}
		if task.Validate() != nil {
			return result, remote.ErrInvalid
		}
		private = task.Private
	}
	// Bind evaluator configuration before submitting original work. Construction
	// is inert; the evaluator reserves resources and resolves credentials only
	// after authenticated canonical completion.
	policy, closeCoordinator, err := configuredReviewPolicy(ctx, configFile, reviewer, maxCost, private)
	if err != nil {
		return result, err
	}
	defer closeCoordinator()
	store, err := remote.OpenRouteStore(routes)
	if err != nil {
		return result, err
	}
	if automatic {
		var choice remote.AutomaticChoice
		result.Dispatch, choice, err = client.DispatchDiscovered(ctx, store, root, key, request, harness.DefaultPolicy(), 0)
		if choice.Version != 0 {
			result.Choice = &choice
		}
		if err == nil {
			task, _, err = store.ResolveAutomatic(key, request)
		}
	} else {
		result.Dispatch, err = client.DispatchRecorded(ctx, store, destination, key, task)
	}
	if err != nil {
		result.Phase = "dispatch_failed_or_unknown"
		return result, err
	}
	result.Phase = "review_pending"
	status, err := client.WatchRecordedEvaluation(ctx, store, root, key, task, policy, wait, 15*time.Second)
	result.Evaluation = &status
	if err == nil {
		result.Phase = "finished"
	}
	return result, err
}
