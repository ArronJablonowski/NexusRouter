package remote

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ArronJablonowski/NexusRouter/evaluation"
)

// ReviewQueue is an opt-in private store of original task requirements for
// restartable review supervision. Unlike RouteStore, it contains prompts.
// Retain it locally; do not expose its files in browser/API projections.
type ReviewQueue struct{ directory string }
type queuedReview struct {
	Version   int                            `json:"version"`
	Automatic *queuedAutomaticReview         `json:"automatic,omitempty"`
	Binding   RouteBinding                   `json:"binding"`
	Task      Task                           `json:"task"`
	Routes    string                         `json:"routes"`
	Evidence  string                         `json:"evidence"`
	Evaluator evaluation.EvaluatorDescriptor `json:"evaluator"`
	Local     bool                           `json:"local"`
	Timeout   time.Duration                  `json:"timeout"`
	Deadline  time.Time                      `json:"deadline"`
}

// ReviewJobStatus is safe metadata. Completed means orchestration finished;
// consult the current review head for verdict/classification, not this receipt.
type ReviewJobStatus struct {
	Version       int    `json:"version"`
	RequestID     string `json:"request_id"`
	JobSHA256     string `json:"job_sha256"`
	Status        string `json:"status"`
	ReviewApplied bool   `json:"review_applied"`
}

func OpenReviewQueue(directory string) (*ReviewQueue, error) {
	if _, err := OpenRouteStore(directory); err != nil {
		return nil, err
	}
	return &ReviewQueue{directory: directory}, nil
}

// OpenExistingReviewQueue is read-only and never creates missing state.
func OpenExistingReviewQueue(directory string) (*ReviewQueue, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, ErrInvalid
	}
	q := &ReviewQueue{directory: directory}
	if err := q.check(); err != nil {
		return nil, err
	}
	return q, nil
}
func (q *ReviewQueue) check() error {
	if q == nil {
		return ErrInvalid
	}
	return (&RouteStore{directory: q.directory}).check()
}
func (q *ReviewQueue) path(key, suffix string) string {
	return filepath.Join(q.directory, hash(key)+suffix)
}
func (q *ReviewQueue) read(key string) (queuedReview, error) {
	var job queuedReview
	if !requestID(key) {
		return job, ErrInvalid
	}
	if err := q.check(); err != nil {
		return job, err
	}
	body, err := readPrivateDocument(q.path(key, ".job.json"), MaxBody*2, false)
	if err != nil {
		return job, err
	}
	if json.Unmarshal(body, &job) != nil || job.key() != key || !job.valid() {
		return queuedReview{}, ErrInvalid
	}
	return job, nil
}
func (q *ReviewQueue) status(job queuedReview) (ReviewJobStatus, error) {
	state := ReviewJobStatus{Version: 1, RequestID: job.key(), JobSHA256: hash(job), Status: "pending"}
	body, err := readPrivateDocument(q.path(state.RequestID, ".result.json"), 4096, false)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	var saved ReviewJobStatus
	if json.Unmarshal(body, &saved) != nil || saved.Version != 1 || saved.RequestID != state.RequestID || saved.JobSHA256 != state.JobSHA256 {
		return state, ErrConflict
	}
	if saved.Status != "completed" && saved.ReviewApplied {
		return state, ErrInvalid
	}
	switch saved.Status {
	case "completed", "attention", "expired", "task_failed", "task_canceled":
	default:
		return state, ErrInvalid
	}
	return saved, nil
}
func (q *ReviewQueue) Status(key string) (ReviewJobStatus, error) {
	job, err := q.read(key)
	if err != nil {
		return ReviewJobStatus{}, err
	}
	return q.status(job)
}

// EnqueueRecordedReview stores one immutable review intent after checking the
// original binding and current authenticated ownership. Exact retries are
// idempotent, including the absolute deadline. It never dispatches or evaluates.
func (c *Client) EnqueueRecordedReview(ctx context.Context, q *ReviewQueue, routes *RouteStore, root, key string, task Task, policy RemoteEvaluator, deadline time.Time) (ReviewJobStatus, error) {
	var zero ReviewJobStatus
	if ctx == nil || ctx.Err() != nil || c == nil || routes == nil || task.Validate() != nil || task.ExpectedHarnessIdentity == nil || deadline.IsZero() || deadline.After(time.Now().Add(24*time.Hour)) || policy.Timeout <= 0 || policy.Timeout > evaluation.MaxReviewDuration {
		return zero, ErrInvalid
	}
	if task.Private && !policy.Local {
		return zero, ErrDenied
	}
	descriptor, err := evaluation.DescribeEvaluator(policy.Evaluator)
	if err != nil {
		return zero, ErrInvalid
	}
	if err = q.check(); err != nil {
		return zero, err
	}
	binding, err := routes.Lookup(key)
	if err != nil {
		return zero, err
	}
	if binding.TaskSHA256 != hash(task) {
		return zero, ErrConflict
	}
	job := queuedReview{Version: 1, Binding: binding, Task: task, Routes: routes.directory, Evidence: root, Evaluator: descriptor, Local: policy.Local, Timeout: policy.Timeout, Deadline: deadline.UTC()}
	if old, e := q.read(key); e == nil {
		if hash(old) != hash(job) {
			return zero, ErrConflict
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return zero, e
	} else if !deadline.After(time.Now()) {
		return zero, ErrInvalid
	}
	if _, err = c.InspectRecorded(ctx, routes, key); err != nil {
		return zero, err
	}
	if err = PrepareOutcomeEvidence(root); err != nil {
		return zero, err
	}
	body, err := json.Marshal(job)
	if err != nil {
		return zero, err
	}
	if err = publishPrivateDocument(q.path(key, ".job.json"), body, MaxBody*2); err != nil {
		return zero, err
	}
	return q.status(job)
}

// ProcessReviewJobs performs one bounded pass, suitable for a daemon-owned loop.
// One local worker owns the queue at a time. A process exit releases ownership;
// pending reads may resume, but durable evaluator admission prevents replay of
// started/uncertain inference. Failed transport/policy attempts become attention
// receipts, never automatic retries. No service installation is performed here.
func (c *Client) ProcessReviewJobs(ctx context.Context, q *ReviewQueue, routes *RouteStore, root string, policy func(bool) (RemoteEvaluator, error)) ([]ReviewJobStatus, error) {
	if ctx == nil || ctx.Err() != nil || c == nil || routes == nil || policy == nil {
		return nil, ErrInvalid
	}
	if err := q.check(); err != nil {
		return nil, err
	}
	release, err := lockReviewQueue(q.directory)
	if err != nil {
		return nil, err
	}
	defer release()
	entries, err := os.ReadDir(q.directory)
	if err != nil {
		return nil, err
	}
	// A bounded store requires explicit archival rather than silently starving
	// jobs past a fixed prefix. Never delete or recycle request identities here.
	if len(entries) > 20001 {
		return nil, ErrUnavailable
	}
	results := []ReviewJobStatus{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".job.json") {
			continue
		}
		if err = ctx.Err(); err != nil {
			return results, err
		}
		body, e := readPrivateDocument(filepath.Join(q.directory, entry.Name()), MaxBody*2, false)
		if e != nil {
			return results, e
		}
		var candidate queuedReview
		if json.Unmarshal(body, &candidate) != nil || entry.Name() != hash(candidate.key())+".job.json" {
			return results, ErrInvalid
		}
		job, e := q.read(candidate.key())
		if e != nil {
			return results, e
		}
		state, e := q.status(job)
		if e != nil {
			return results, e
		}
		if state.Status != "pending" {
			results = append(results, state)
			continue
		}
		state = c.processReviewJob(ctx, routes, root, policy, job, state)

		// Shutdown does not invent a terminal result. If evaluation already began,
		// its own immutable admission/result remains authoritative on restart.
		if err = ctx.Err(); err != nil {
			return results, err
		}
		if state.Status != "pending" {
			encoded, _ := json.Marshal(state)
			if err = publishPrivateDocument(q.path(state.RequestID, ".result.json"), encoded, 4096); err != nil {
				return results, err
			}
		}
		results = append(results, state)
	}
	return results, nil
}
