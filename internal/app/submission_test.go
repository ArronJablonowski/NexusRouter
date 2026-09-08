package app

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

func submissionService(t *testing.T) *Service {
	t.Helper()
	cfg := config.Defaults()
	cfg.Telemetry.Database = filepath.Join(t.TempDir(), "submission.db")
	return &Service{settings: cfg}
}

func TestSubmissionIntakeIdempotencyAndCancellation(t *testing.T) {
	s := submissionService(t)
	ctx := context.Background()
	r := Request{ModelID: "fixture", Prompt: "hello"}
	a, err := s.Submit(ctx, "0123456789abcdef", r)
	if err != nil || a.State != "queued" {
		t.Fatal(a, err)
	}
	b, err := s.Submit(ctx, "0123456789abcdef", r)
	if err != nil || b.ID != a.ID {
		t.Fatal(b, err)
	}
	r.Prompt = "changed"
	if _, err := s.Submit(ctx, "0123456789abcdef", r); !errors.Is(err, submissions.ErrConflict) {
		t.Fatal(err)
	}
	c, err := s.CancelSubmission(ctx, a.ID)
	if err != nil || c.State != "canceled" {
		t.Fatal(c, err)
	}
}

func TestResumeSubmissionRequiresExistingExactRequestAndConfiguration(t *testing.T) {
	s := submissionService(t)
	ctx := context.Background()
	request := Request{ModelID: "fixture", Prompt: "hello"}
	created, err := s.Submit(ctx, "resume-exact-key", request)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := s.ResumeSubmission(ctx, "resume-exact-key", request)
	if err != nil || resumed.ID != created.ID {
		t.Fatal(resumed, err)
	}
	changed := request
	changed.Prompt = "changed"
	if _, err := s.ResumeSubmission(ctx, "resume-exact-key", changed); !errors.Is(err, submissions.ErrConflict) {
		t.Fatal(err)
	}
	s.settings.Workers.Max++
	if _, err := s.ResumeSubmission(ctx, "resume-exact-key", request); !errors.Is(err, submissions.ErrConflict) {
		t.Fatal(err)
	}
	missing := submissionService(t)
	if _, err := missing.ResumeSubmission(ctx, "resume-missing-key", request); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if _, err := os.Stat(missing.settings.Telemetry.Database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("resume created storage", err)
	}
}

func TestSubmissionRejectsSecretsAndInvalidKeysWithoutStorage(t *testing.T) {
	s := submissionService(t)
	s.secret = func(string) string { return "private\"credential" }
	for _, key := range []string{"short", "0123456789abcde\n", "0123456789abcdef"} {
		_, err := s.Submit(context.Background(), key, Request{ModelID: "fixture", Prompt: "private\"credential"})
		if !errors.Is(err, ErrAdmission) {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(s.settings.Telemetry.Database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestSubmissionMissingStatusDoesNotCreateStorage(t *testing.T) {
	s := submissionService(t)
	if _, err := s.CancelSubmission(context.Background(), "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.settings.Telemetry.Database); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestSubmissionPrivateEnvelopeRejectsDuplicateAndUnknown(t *testing.T) {
	for _, body := range []string{`{"version":1,"version":1,"request":{"ModelID":"fixture","Prompt":"hello"}}`, `{"version":1,"request":{"ModelID":"fixture","Prompt":"hello","private":"value"}}`} {
		if _, err := decodeSubmission([]byte(body)); !errors.Is(err, ErrAdmission) {
			t.Fatal(err)
		}
	}
}
