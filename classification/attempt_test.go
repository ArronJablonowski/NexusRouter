package classification

import (
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/providers"
)

func validAttempt() Attempt {
	start := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	return Attempt{
		Version: 1, ID: "classifier-attempt", TaskID: "task", SessionID: "session",
		SubmissionID: "submission", RequestDigest: strings.Repeat("a", 64), ConfigID: strings.Repeat("b", 64),
		Model: "small-model", Provider: "local-provider", EstimatedCost: 0.01,
		Status: AttemptCompleted, StartedAt: start, FinishedAt: start.Add(time.Second), Elapsed: time.Second,
		Usage:    &providers.Usage{InputTokens: 10, OutputTokens: 4},
		Decision: &Decision{Version: 1, Domain: "code", Capabilities: []string{"code", "review"}},
	}
}

func TestAttemptValidateLifecycle(t *testing.T) {
	completed := validAttempt()
	if err := completed.Validate(); err != nil {
		t.Fatal(err)
	}
	started := completed
	started.Status, started.FinishedAt, started.Elapsed, started.Usage, started.Decision = AttemptStarted, time.Time{}, 0, nil, nil
	if err := started.Validate(); err != nil {
		t.Fatal(err)
	}
	failed := completed
	failed.Status, failed.Code, failed.Decision = AttemptFailed, CodeInvalidResponse, nil
	if err := failed.Validate(); err != nil {
		t.Fatal(err)
	}
	canceled := failed
	canceled.Status, canceled.Code, canceled.Usage, canceled.Elapsed = AttemptCanceled, CodeCanceled, nil, 0
	if err := canceled.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAttemptRejectsInvalidLifecycleAndSensitiveUnboundedFields(t *testing.T) {
	tests := map[string]func(*Attempt){
		"version":       func(a *Attempt) { a.Version = 2 },
		"id":            func(a *Attempt) { a.ID = "bad/id" },
		"submission":    func(a *Attempt) { a.SubmissionID = "bad/id" },
		"digest":        func(a *Attempt) { a.RequestDigest = strings.Repeat("A", 64) },
		"model":         func(a *Attempt) { a.Model = "model\nsecret" },
		"negative cost": func(a *Attempt) { a.EstimatedCost = -1 },
		"local time":    func(a *Attempt) { a.StartedAt = a.StartedAt.In(time.FixedZone("offset", 3600)) },
		"before start":  func(a *Attempt) { a.FinishedAt = a.StartedAt.Add(-time.Second) },
		"long elapsed": func(a *Attempt) {
			a.FinishedAt = a.StartedAt.Add(MaxClassifierTimeout + time.Second)
			a.Elapsed = MaxClassifierTimeout + time.Second
		},
		"elapsed exceeds wall":   func(a *Attempt) { a.Elapsed = 2 * time.Second },
		"negative usage":         func(a *Attempt) { a.Usage.InputTokens = -1 },
		"completed code":         func(a *Attempt) { a.Code = CodeProviderFailed },
		"completed no decision":  func(a *Attempt) { a.Decision = nil },
		"unsorted capabilities":  func(a *Attempt) { a.Decision.Capabilities = []string{"review", "code"} },
		"duplicate capabilities": func(a *Attempt) { a.Decision.Capabilities = []string{"code", "code"} },
		"nil capabilities":       func(a *Attempt) { a.Decision.Capabilities = nil },
		"failed decision":        func(a *Attempt) { a.Status = AttemptFailed; a.Code = CodeProviderFailed },
		"failed unknown code":    func(a *Attempt) { a.Status = AttemptFailed; a.Code = "raw-provider-error"; a.Decision = nil },
		"canceled wrong code":    func(a *Attempt) { a.Status = AttemptCanceled; a.Code = CodeTimeout; a.Decision = nil },
		"unknown status":         func(a *Attempt) { a.Status = "done" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			a := validAttempt()
			mutate(&a)
			if a.Validate() == nil {
				t.Fatalf("accepted %#v", a)
			}
		})
	}
}

func TestAttemptStartedHasNoTerminalData(t *testing.T) {
	base := validAttempt()
	base.Status, base.FinishedAt, base.Elapsed, base.Usage, base.Decision = AttemptStarted, time.Time{}, 0, nil, nil
	tests := []func(*Attempt){
		func(a *Attempt) { a.Code = CodeProviderFailed },
		func(a *Attempt) { a.FinishedAt = a.StartedAt },
		func(a *Attempt) { a.Elapsed = time.Nanosecond },
		func(a *Attempt) { a.Usage = &providers.Usage{} },
		func(a *Attempt) { a.Decision = &Decision{Version: 1, Domain: "code", Capabilities: []string{}} },
	}
	for index, mutate := range tests {
		a := base
		mutate(&a)
		if a.Validate() == nil {
			t.Fatalf("case %d accepted", index)
		}
	}
}
