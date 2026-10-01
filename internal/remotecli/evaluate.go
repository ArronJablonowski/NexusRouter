package remotecli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"math"
	"os"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/remote"
)

func evaluateOperation(ctx context.Context, client *remote.Client, operation, routes, root, key, configFile, reviewer string, maxCost float64, input io.Reader) (remote.RemoteEvaluationStatus, error) {
	return evaluateOperationWithWait(ctx, client, operation, routes, root, key, configFile, reviewer, maxCost, input, 0)
}

func evaluateOperationWithWait(ctx context.Context, client *remote.Client, operation, routes, root, key, configFile, reviewer string, maxCost float64, input io.Reader, wait time.Duration) (remote.RemoteEvaluationStatus, error) {
	var zero remote.RemoteEvaluationStatus
	if wait < 0 || wait > 24*time.Hour || configFile == "" || reviewer == "" || maxCost < 0 || math.IsNaN(maxCost) || math.IsInf(maxCost, 0) || input == nil {
		return zero, remote.ErrInvalid
	}
	body, err := io.ReadAll(io.LimitReader(input, remote.MaxBody+1))
	if err != nil || len(body) > remote.MaxBody {
		return zero, remote.ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var task remote.Task
	var request remote.AutomaticRequest
	if operation == "auto-evaluate" {
		if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
			return zero, remote.ErrInvalid
		}
	} else if operation == "evaluate" {
		if decoder.Decode(&task) != nil || decoder.Decode(new(any)) != io.EOF || task.Validate() != nil {
			return zero, remote.ErrInvalid
		}
	} else {
		return zero, remote.ErrInvalid
	}
	store, err := remote.OpenRouteStore(routes)
	if err != nil {
		return zero, err
	}
	if operation == "auto-evaluate" {
		task, _, err = store.ResolveAutomatic(key, request)
		if err != nil {
			return zero, err
		}
	}
	cfg, err := config.Load(config.Options{ProjectFile: configFile})
	if err != nil {
		return zero, remote.ErrInvalid
	}
	service, err := app.NewService(cfg, os.Getenv)
	if err != nil {
		return zero, remote.ErrInvalid
	}
	evaluator, local, err := service.ConfiguredEvaluator(reviewer, maxCost, task.Private)
	if err != nil {
		return zero, remote.ErrDenied
	}
	closeCoordinator, err := app.InstallHostResourceCoordinator(ctx, service, "remote-review-"+rand.Text())
	if err != nil {
		return zero, remote.ErrUnavailable
	}
	defer closeCoordinator()
	policy := remote.RemoteEvaluator{Evaluator: evaluator, Local: local, Timeout: time.Minute}
	if wait > 0 {
		return client.WatchRecordedEvaluation(ctx, store, root, key, task, policy, wait, 15*time.Second)
	}
	return client.EvaluateRecordedOutcome(ctx, store, root, key, task, policy)
}
