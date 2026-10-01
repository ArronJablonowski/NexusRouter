package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

// AutomaticRequest is saved by the caller before submission. Routing describes
// the task, never an advertised remote quality score. Its exact digest is bound
// to the first durable choice; subsequent candidate/score changes cannot reroute.
type AutomaticRequest struct {
	Version int
	Prompt  string
	Routing harness.Request
}

// AutomaticChoice stores no prompt. Preserve the original AutomaticRequest to
// recover the exact pinned Task and reconcile/review its eventual result.
type AutomaticChoice struct {
	Version           int              `json:"version"`
	RequestID         string           `json:"request_id"`
	IntentSHA256      string           `json:"intent_sha256"`
	CallerFingerprint string           `json:"caller_fingerprint"`
	Destination       string           `json:"destination"`
	ModelID           string           `json:"model_id"`
	HarnessID         string           `json:"harness_id"`
	Identity          harness.Identity `json:"identity"`
	SelectionSHA256   string           `json:"selection_sha256"`
	Score             harness.Ranked   `json:"score"`
	Reason            string           `json:"reason"`
	Explored          bool             `json:"explored"`
	SelectedAt        time.Time        `json:"selected_at"`
}

func (a AutomaticChoice) valid() bool {
	return a.Version == Version && requestID(a.RequestID) && hexDigest(a.IntentSHA256) && hexDigest(a.CallerFingerprint) && id(a.Destination) && name(a.ModelID) && name(a.HarnessID) && a.HarnessID != "auto" && a.Identity.Validate() == nil && hexDigest(a.SelectionSHA256) && a.SelectedAt.Location() == time.UTC && a.SelectedAt.Year() >= 1970 && a.SelectedAt.Year() < 2261
}
func (a AutomaticChoice) Task(request AutomaticRequest) (Task, error) {
	var zero Task
	if !a.valid() || request.Version != Version || a.IntentSHA256 != hash(request) {
		return zero, ErrConflict
	}
	decision := harness.Selection{Version: 1, Task: request.Routing.Task, AsOf: a.SelectedAt, Primary: a.Score, Ranked: []harness.Ranked{a.Score}, Reason: a.Reason, Explored: a.Explored}
	if a.Score.Identity != a.Identity || decision.Validate() != nil {
		return zero, ErrInvalid
	}
	i := a.Identity
	r := request.Routing
	task := Task{Version: 1, ModelID: a.ModelID, HarnessID: a.HarnessID, HarnessDifficulty: r.Task.Difficulty, ExpectedHarnessIdentity: &i, Prompt: request.Prompt, Domain: r.Task.Domain, Profile: r.Task.Profile, ContextTokens: int(r.ContextTokens), MaxCost: r.MaxCost, Private: r.LocalRequired}
	if task.Validate() != nil {
		return zero, ErrInvalid
	}
	return task, nil
}
func (s *RouteStore) choicePath(key string) string {
	return filepath.Join(s.directory, certificateDigest([]byte(key))+".choice.json")
}
func (s *RouteStore) AutomaticChoice(key string) (AutomaticChoice, error) {
	var out AutomaticChoice
	if !requestID(key) {
		return out, ErrInvalid
	}
	if err := s.check(); err != nil {
		return out, err
	}
	path := s.choicePath(key)
	st, err := os.Lstat(path)
	if err != nil {
		return out, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 32768 {
		return out, ErrDenied
	}
	f, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(st, actual) {
		return out, ErrDenied
	}
	decoder := json.NewDecoder(io.LimitReader(f, 32769))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil || decoder.Decode(new(any)) != io.EOF || !out.valid() || out.RequestID != key {
		return AutomaticChoice{}, ErrInvalid
	}
	return out, nil
}

// DispatchAutomatic ranks only an unbound request. Before network dispatch it
// atomically commits the choice, then commits the ordinary route binding. A
// restarted caller recovers that choice without querying alternate candidates.
// Lost responses, changed rankings and admission failures never trigger fallback.
// Candidates must still come from the embedding host's fresh admission checks.
func (c *Client) DispatchAutomatic(ctx context.Context, routes *RouteStore, evidenceRoot, key string, request AutomaticRequest, policy harness.Policy, candidates []DestinationCandidate, draw float64) (submissions.Status, AutomaticChoice, error) {
	var status submissions.Status
	var choice AutomaticChoice
	if c == nil || ctx == nil || ctx.Err() != nil || routes == nil || !requestID(key) || request.Version != Version || len(request.Prompt) == 0 || len(request.Prompt) > MaxBody/2 || request.Routing.ContextTokens < 8192 || request.Routing.ContextTokens > 1<<24 {
		return status, choice, ErrInvalid
	}
	// Validate saved task semantics independently of today's selection policy.
	if _, err := harness.SelectScoped(request.Routing, harness.DefaultPolicy(), nil, time.Now().UTC(), 0); err != nil && !errors.Is(err, harness.ErrNoRoute) {
		return status, choice, err
	}
	cert, _, err := c.Credentials.load()
	if err != nil || len(cert.Certificate) == 0 {
		return status, choice, ErrDenied
	}
	pin := certificateDigest(cert.Certificate[0])
	choice, err = routes.AutomaticChoice(key)
	if errors.Is(err, os.ErrNotExist) {
		// A manually bound/ambiguous request is not a new automatic selection.
		if _, e := routes.Lookup(key); !errors.Is(e, os.ErrNotExist) {
			if e == nil {
				e = ErrConflict
			}
			return status, choice, e
		}
		selected, e := c.RankRecordedCandidates(ctx, evidenceRoot, request.Routing, policy, candidates, draw)
		if e != nil {
			return status, choice, e
		}
		if selected.CallerFingerprint != pin {
			return status, choice, ErrConflict
		}
		choice = AutomaticChoice{Version: Version, RequestID: key, IntentSHA256: hash(request), CallerFingerprint: pin, Destination: selected.Selection.Primary.Scope, ModelID: selected.ModelID, HarnessID: selected.HarnessID, Identity: selected.Selection.Primary.Ranked.Identity, SelectionSHA256: hash(selected), SelectedAt: selected.Selection.AsOf, Score: selected.Selection.Primary.Ranked, Reason: selected.Selection.Reason, Explored: selected.Selection.Explored}
		if _, e = choice.Task(request); e != nil {
			return status, AutomaticChoice{}, e
		}
		body, e := json.Marshal(choice)
		if e != nil {
			return status, AutomaticChoice{}, e
		}
		e = immutableReceipt(routes, routes.choicePath(key), body)
		if e != nil && !errors.Is(e, ErrConflict) {
			return status, choice, e
		}
		// A concurrent chooser may have committed first. Recover its exact intent,
		// not a second destination. Corrupt or different-intent winners reject below.
		choice, err = routes.AutomaticChoice(key)
	}
	if err != nil {
		return status, choice, err
	}
	if choice.CallerFingerprint != pin {
		return status, choice, ErrConflict
	}
	task, err := choice.Task(request)
	if err != nil {
		return status, choice, err
	}
	// Sync on recovery too: prior publication might have lost its sync response.
	if err = routes.syncDirectory(); err != nil {
		return status, choice, err
	}
	binding := RouteBinding{Version, key, choice.Destination, pin, hash(task)}
	if err = routes.Bind(binding); err != nil {
		return status, choice, err
	}
	// Pin the original caller even if credentials rotate between these checks.
	err = c.callPinned(ctx, choice.Destination, "dispatch", "POST", "/v1/remote/tasks/"+key, &task, nil, &status, pin)
	return status, choice, err
}
