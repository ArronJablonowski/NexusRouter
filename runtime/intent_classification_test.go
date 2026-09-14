package runtime

import (
	"strings"
	"testing"
	"time"
)

func validIntentClassificationUse() *IntentClassificationUse {
	return &IntentClassificationUse{
		Version: 1, AttemptID: "classifier-attempt", Status: "completed",
		DecisionDigest: strings.Repeat("a", 64),
	}
}

func TestIntentClassificationUseValidation(t *testing.T) {
	if err := validIntentClassificationUse().Validate(); err != nil {
		t.Fatal(err)
	}
	failed := IntentClassificationUse{Version: 1, AttemptID: "classifier-attempt", Status: "failed", Code: "invalid_response"}
	if err := failed.Validate(); err != nil {
		t.Fatal(err)
	}
	canceled := IntentClassificationUse{Version: 1, AttemptID: "classifier-attempt", Status: "canceled", Code: "canceled"}
	if err := canceled.Validate(); err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*IntentClassificationUse){
		"version":          func(u *IntentClassificationUse) { u.Version = 2 },
		"attempt":          func(u *IntentClassificationUse) { u.AttemptID = "raw/attempt" },
		"status":           func(u *IntentClassificationUse) { u.Status = "started" },
		"completed code":   func(u *IntentClassificationUse) { u.Code = "provider_failed" },
		"missing digest":   func(u *IntentClassificationUse) { u.DecisionDigest = "" },
		"uppercase digest": func(u *IntentClassificationUse) { u.DecisionDigest = strings.Repeat("A", 64) },
		"unknown code": func(u *IntentClassificationUse) {
			u.Status, u.Code, u.DecisionDigest = "failed", "provider said secret", ""
		},
		"failed digest": func(u *IntentClassificationUse) { u.Status, u.Code = "failed", "timeout" },
		"canceled code": func(u *IntentClassificationUse) {
			u.Status, u.Code, u.DecisionDigest = "canceled", "timeout", ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			use := validIntentClassificationUse()
			mutate(use)
			if use.Validate() == nil {
				t.Fatalf("invalid use accepted: %#v", use)
			}
		})
	}
}

func TestIntentClassificationAndCapabilitiesOnlyAppearOnTaskStarted(t *testing.T) {
	base := Event{
		Version: 1, ID: "event", TaskID: "task", SessionID: "session",
		CorrelationID: "task", Sequence: 1, Time: time.Now().UTC(), Kind: TaskStarted,
		Data: Data{IntentClassification: validIntentClassificationUse(), Capabilities: []string{"chat", "code"}},
	}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}

	wrongKind := base
	wrongKind.Kind = TaskCompleted
	if wrongKind.Validate() == nil {
		t.Fatal("classification attribution accepted outside task.started")
	}
	wrongKind = base
	wrongKind.Kind = TaskCompleted
	wrongKind.Data.IntentClassification = nil
	if wrongKind.Validate() == nil {
		t.Fatal("capabilities accepted outside task.started")
	}

	for name, capabilities := range map[string][]string{
		"empty":     {""},
		"duplicate": {"chat", "chat"},
		"space":     {" chat"},
		"control":   {"chat\nsecret"},
	} {
		t.Run(name, func(t *testing.T) {
			event := base
			event.Data.IntentClassification = nil
			event.Data.Capabilities = capabilities
			if event.Validate() == nil {
				t.Fatalf("invalid capabilities accepted: %#v", capabilities)
			}
		})
	}
}
