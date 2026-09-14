package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

func legacySubmissionConfigDigest(t *testing.T, s *Service) string {
	t.Helper()
	body, err := json.Marshal(s.settings)
	if err != nil {
		t.Fatal(err)
	}
	return submissionDigest(body)
}

func TestSubmissionContractGenerationFencesLegacyQueuedIntent(t *testing.T) {
	ctx := context.Background()
	s, db, calls := recoveryFixture(t)
	request := Request{ModelID: "chat", Prompt: "legacy blank intent"}
	body, err := json.Marshal(submissionEnvelope{Version: 1, Request: request})
	if err != nil {
		t.Fatal(err)
	}
	legacyDigest := legacySubmissionConfigDigest(t, s)
	currentDigest := s.submissionConfigDigest()
	if legacyDigest == currentDigest {
		t.Fatal("versioned submission contract did not change the legacy configuration digest")
	}
	created, err := db.CreateSubmission(ctx, submissionDigest([]byte("legacy-contract-key")), submissionDigest(body), legacyDigest, body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeSubmission(body); !errors.Is(err, ErrAdmission) {
		t.Fatal("legacy blank-intent envelope crossed the current contract", err)
	}
	if _, err := db.ClaimSubmission(ctx, currentDigest, time.Now().UTC(), time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("new contract claimed legacy work", err)
	}
	if _, err := s.ResumeSubmission(ctx, "legacy-contract-key", request); !errors.Is(err, submissions.ErrConflict) {
		t.Fatal("legacy request resumed across contract generations", err)
	}
	if _, err := (&Dispatcher{db: db}).reconcileConfigurationPage(ctx, currentDigest, ""); err != nil {
		t.Fatal("configuration reconciliation", err)
	}
	retired, err := s.SubmissionStatus(ctx, created.ID)
	if err != nil || retired.State != "failed" || retired.ErrorCode != "configuration_changed" || len(retired.TaskIDs) != 0 || calls.Load() != 0 {
		t.Fatal("legacy submission was not retired without execution", retired, err, calls.Load())
	}
}

func TestSubmissionContractGenerationKeepsCanonicalIdempotencyStable(t *testing.T) {
	ctx := context.Background()
	s := submissionService(t)
	request := Request{Prompt: "canonical request"}
	created, err := s.Submit(ctx, "canonical-contract-key", request)
	if err != nil {
		t.Fatal(err)
	}
	restarted := &Service{settings: s.settings}
	if restarted.submissionConfigDigest() != s.submissionConfigDigest() {
		t.Fatal("submission contract digest changed across equivalent service instances")
	}
	resumed, err := restarted.Submit(ctx, "canonical-contract-key", request)
	if err != nil || resumed.ID != created.ID || resumed.ConfigDigest != restarted.submissionConfigDigest() {
		t.Fatal("canonical idempotency changed across restart", resumed, err)
	}
}
