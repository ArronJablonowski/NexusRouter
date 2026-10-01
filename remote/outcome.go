package remote

import (
	"context"
	"strconv"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

// OutcomeReceipt binds authenticated completion to saved intent. It contains no
// prompt or result text. The destination is a trusted paired runtime, not a
// cryptographic attestation of its operating system or model weights.
type OutcomeReceipt struct {
	Version      int               `json:"version"`
	Route        RouteBinding      `json:"route"`
	SubmissionID string            `json:"submission_id"`
	EventsSHA256 string            `json:"events_sha256"`
	Execution    harness.Execution `json:"execution"`
}

// VerifiedOutcome can only be produced by authenticating the saved destination
// and checking its canonical completed journal. Copies of Receipt are for audit,
// not an import API for remote-advertised quality scores.
type VerifiedOutcome struct {
	receipt  OutcomeReceipt
	verified bool
	output   string
}

// Output returns content checked against the canonical completion hash. It is
// held only in memory; Record persists hashes and attribution, never this text.
func (v VerifiedOutcome) Output() string {
	if !v.verified {
		return ""
	}
	return v.output
}

func (v VerifiedOutcome) Receipt() OutcomeReceipt { return v.receipt }

// RecordedOutcome performs reads only. It never dispatches, retries inference,
// reroutes, grades content, or trusts an advertised quality score.
func (c *Client) RecordedOutcome(ctx context.Context, routes *RouteStore, key string, task Task) (VerifiedOutcome, error) {
	var out VerifiedOutcome
	if c == nil || ctx == nil || ctx.Err() != nil || routes == nil || task.Validate() != nil || task.ExpectedHarnessIdentity == nil {
		return out, ErrInvalid
	}
	expected := *task.ExpectedHarnessIdentity
	task.ExpectedHarnessIdentity = &expected
	binding, err := routes.Lookup(key)
	if err != nil {
		return out, err
	}
	if binding.TaskSHA256 != hash(task) {
		return out, ErrConflict
	}
	var status submissions.Status
	path := "/v1/remote/tasks/" + key
	err = c.callPinned(ctx, binding.Destination, "inspect", "GET", path, nil, nil, &status, binding.CallerFingerprint)
	if err != nil {
		return out, err
	}
	if status.Version != 1 || status.ID == "" || status.State != "succeeded" || len(status.TaskIDs) != 1 || status.Result == nil || status.Result.TaskID != status.TaskIDs[0] || !name(status.TaskIDs[0]) {
		return out, ErrUnavailable
	}
	events, err := readOutcomePages(func(after int64) (sessions.EventPage, error) {
		var page sessions.EventPage
		err := c.callPinned(ctx, binding.Destination, "inspect", "GET", path+"/events", nil, map[string]string{"X-Nexus-Task": status.TaskIDs[0], "X-Nexus-After": strconv.FormatInt(after, 10)}, &page, binding.CallerFingerprint)
		return page, err
	}, status.TaskIDs[0])
	if err != nil {
		return out, err
	}
	execution, err := validateRecordedOutcome(task, status, events)
	if err != nil {
		return out, err
	}
	out.receipt = OutcomeReceipt{Version, binding, status.ID, hash(events), execution}
	out.output = status.Result.Text
	out.verified = true
	return out, nil
}

func readOutcomePages(read func(int64) (sessions.EventPage, error), task string) ([]runtime.Event, error) {
	var events []runtime.Event
	var head int64
	var session string
	after, total := int64(0), 0
	for {
		p, err := read(after)
		if err != nil {
			return nil, err
		}
		if p.Validate() != nil || p.TaskID != task || p.FromSequence != after || p.State != "completed" || p.HeadSequence > runtime.MaxHarnessAgentEvents {
			return nil, ErrInvalid
		}
		if after == 0 {
			head, session = p.HeadSequence, p.SessionID
		}
		if p.HeadSequence != head || p.SessionID != session {
			return nil, ErrConflict
		}
		for _, e := range p.Events {
			body, err := e.Encode()
			if err != nil || len(body) > (32<<20)-total {
				return nil, ErrInvalid
			}
			total += len(body)
			events = append(events, e)
		}
		if !p.HasMore {
			return events, nil
		}
		if p.NextSequence <= after {
			return nil, ErrInvalid
		}
		after = p.NextSequence
	}
}

func validateRecordedOutcome(task Task, status submissions.Status, events []runtime.Event) (harness.Execution, error) {
	var zero harness.Execution
	if task.ExpectedHarnessIdentity == nil || status.Result == nil || len(status.TaskIDs) != 1 {
		return zero, ErrInvalid
	}
	actual, err := runtime.ValidateHarnessOutcome(events, status.TaskIDs[0])
	if err != nil {
		return zero, err
	}
	profile, difficulty := task.Profile, task.HarnessDifficulty
	if profile == "" {
		profile = "default"
	}
	if difficulty == "" {
		difficulty = "unknown"
	}
	first := events[0].Data
	if actual.Actual != *task.ExpectedHarnessIdentity || actual.Task != (harness.TaskClass{Domain: task.Domain, Profile: profile, Difficulty: difficulty}) || actual.Status != "completed" || actual.ID != status.TaskIDs[0] || actual.OutputSHA256 != certificateDigest([]byte(status.Result.Text)) || first.SubmissionID != status.ID || first.ContextTokens != task.ContextTokens {
		return zero, ErrConflict
	}
	// Remote dispatch supplies one user prompt; system instructions may precede it.
	// Require the final input message to be that exact prompt and forbid extra users.
	users := 0
	for _, m := range first.Messages {
		if m.Role == "user" {
			users++
			if m.Content != task.Prompt {
				return zero, ErrConflict
			}
		}
	}
	if users != 1 || len(first.Messages) == 0 || first.Messages[len(first.Messages)-1].Role != "user" || (task.Private && first.Privacy != "local_only") {
		return zero, ErrConflict
	}
	return actual, nil
}
