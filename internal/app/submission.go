package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

var ErrSubmission = errors.New("submission unavailable")

type submissionEnvelope struct {
	Version int     `json:"version"`
	Request Request `json:"request"`
}

func submissionDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func (s *Service) submissionConfigDigest() string {
	body, _ := json.Marshal(s.settings)
	return submissionDigest(body)
}

func (s *Service) Submit(ctx context.Context, key string, r Request) (submissions.Status, error) {
	if len(s.toolExtension.Names()) > 0 || s.settings.Tools.ReplaceEnabled {
		return submissions.Status{}, ErrAdmission
	}
	if len(key) < 16 || len(key) > 128 || strings.ContainsFunc(key, func(c rune) bool { return c < 33 || c > 126 }) || validateInput(r) != nil {
		return submissions.Status{}, ErrAdmission
	}
	body, err := json.Marshal(submissionEnvelope{1, r})
	if err != nil || len(body) > 8<<20 {
		return submissions.Status{}, ErrAdmission
	}
	// Reject rather than rewrite secret-bearing intent. Check the JSON-escaped
	// representation too, since credentials may contain quotes or controls.
	if s.secret != nil {
		names := []string{"DARWIN_API_TOKEN"}
		for _, p := range s.settings.Providers {
			if p.APIKeyEnv != "" {
				names = append(names, p.APIKeyEnv)
			}
		}
		if m := s.settings.Telemetry.MetricsExport; m != nil && m.APIKeyEnv != "" {
			names = append(names, m.APIKeyEnv)
		}
		if trace := s.settings.Telemetry.TraceExport; trace != nil && trace.APIKeyEnv != "" {
			names = append(names, trace.APIKeyEnv)
		}
		for _, name := range names {
			secret := s.secret(name)
			if secret == "" {
				continue
			}
			escaped, _ := json.Marshal(secret)
			if bytes.Contains(body, []byte(secret)) || bytes.Contains(body, escaped[1:len(escaped)-1]) {
				return submissions.Status{}, ErrAdmission
			}
		}
	}
	db, err := telemetry.Open(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return submissions.Status{}, ErrSubmission
	}
	defer db.Close()
	status, err := db.CreateSubmission(ctx, submissionDigest([]byte(key)), submissionDigest(body), s.submissionConfigDigest(), body)
	return status, submissionError(err)
}

func (s *Service) SubmissionStatus(ctx context.Context, id string) (submissions.Status, error) {
	if !sessions.ValidEventPageID(id) {
		return submissions.Status{}, ErrAdmission
	}
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return submissions.Status{}, sql.ErrNoRows
		}
		return submissions.Status{}, ErrSubmission
	}
	defer db.Close()
	status, err := db.Submission(ctx, id)
	return status, submissionError(err)
}

func (s *Service) CancelSubmission(ctx context.Context, id string) (submissions.Status, error) {
	if _, err := s.SubmissionStatus(ctx, id); err != nil {
		return submissions.Status{}, err
	}
	db, err := telemetry.Open(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return submissions.Status{}, ErrSubmission
	}
	defer db.Close()
	status, err := db.CancelSubmission(ctx, id)
	return status, submissionError(err)
}

func submissionError(err error) error {
	for _, known := range []error{sql.ErrNoRows, telemetry.ErrConflict, submissions.ErrConflict, submissions.ErrCapacity, submissions.ErrInvalid, submissions.ErrLeaseLost, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, known) {
			return known
		}
	}
	if err != nil {
		return ErrSubmission
	}
	return nil
}

func decodeSubmission(body []byte) (Request, error) {
	var envelope submissionEnvelope
	if len(body) > 8<<20 {
		return Request{}, ErrAdmission
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF || envelope.Version != 1 || validateInput(envelope.Request) != nil {
		return Request{}, ErrAdmission
	}
	// Only the intake's canonical representation is accepted: this additionally
	// rejects duplicate keys and differently cased aliases accepted by encoding/json.
	canonical, err := json.Marshal(envelope)
	if err != nil || !bytes.Equal(body, canonical) {
		return Request{}, ErrAdmission
	}
	return envelope.Request, nil
}
