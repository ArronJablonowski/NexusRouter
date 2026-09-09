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
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/submissions"
)

var ErrSubmission = errors.New("submission unavailable")

// submissionContractVersion fences durable queued work from binaries whose
// admission or canonicalization semantics differ. Increment it whenever a
// change can reinterpret a persisted submission request.
const submissionContractVersion = 1

type submissionEnvelope struct {
	Version int                            `json:"version"`
	Request Request                        `json:"request"`
	Branch  *submissions.BranchSourceFence `json:"branch,omitempty"`
	Resume  *submissions.ResumeSourceFence `json:"resume,omitempty"`
}

func submissionDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func (s *Service) submissionConfigDigest() string {
	body, _ := json.Marshal(struct {
		Version  int             `json:"version"`
		Settings config.Settings `json:"settings"`
	}{Version: submissionContractVersion, Settings: s.settings})
	return submissionDigest(body)
}

func (s *Service) submissionPayload(key string, r Request) (string, string, []byte, error) {
	return s.submissionEnvelopePayload(key, submissionEnvelope{Version: 1, Request: r})
}

func (s *Service) submissionEnvelopePayload(key string, envelope submissionEnvelope) (string, string, []byte, error) {
	r, err := classifyRequestIntent(envelope.Request)
	if err != nil {
		return "", "", nil, err
	}
	envelope.Request = r
	if len(s.toolExtension.Names()) > 0 || s.settings.Tools.ReplaceEnabled {
		return "", "", nil, ErrAdmission
	}
	if len(key) < 16 || len(key) > 128 || strings.ContainsFunc(key, func(c rune) bool { return c < 33 || c > 126 }) || validateInput(r) != nil {
		return "", "", nil, ErrAdmission
	}
	if envelope.Version != 1 || envelope.Branch != nil && envelope.Branch.Validate() != nil || envelope.Resume != nil && envelope.Resume.Validate() != nil || envelope.Branch != nil && envelope.Resume != nil {
		return "", "", nil, ErrAdmission
	}
	body, err := json.Marshal(envelope)
	if err != nil || len(body) > 8<<20 {
		return "", "", nil, ErrAdmission
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
				return "", "", nil, ErrAdmission
			}
		}
	}
	return submissionDigest([]byte(key)), submissionDigest(body), body, nil
}

// SubmitResume admits a fresh task from one exact recovered model or
// delegation checkpoint. The caller must supply new prompt intent; recovery
// output is context only and no interrupted provider or tool call is replayed.
func (s *Service) SubmitResume(ctx context.Context, key string, source sessions.TaskHeadFence, r Request) (submissions.Status, error) {
	if source.Validate() != nil || strings.TrimSpace(r.Prompt) == "" || len(r.Messages) != 0 || r.ContinueTaskID != "" || r.Compaction != nil || r.SummaryAttemptID != "" {
		return submissions.Status{}, ErrAdmission
	}
	r.ContinueTaskID = source.TaskID
	// Reject malformed public intent and secret-bearing input before opening or
	// creating storage. The durable resume authority is derived afterward.
	if _, _, _, err := s.submissionPayload(key, r); err != nil {
		return submissions.Status{}, err
	}
	db, err := telemetry.Open(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return submissions.Status{}, ErrSubmission
	}
	defer db.Close()
	fence, err := db.ResumeSource(ctx, source.TaskID)
	if err != nil {
		return submissions.Status{}, submissionError(err)
	}
	if fence.TaskID != source.TaskID || fence.SessionID != source.SessionID || fence.HeadSequence != source.HeadSequence || fence.HeadEventID != source.HeadEventID {
		return submissions.Status{}, ErrAdmission
	}
	if fence.SourcePrivacy != "cloud_allowed" || r.LocalRequired {
		fence.EffectivePrivacy = "local_only"
	} else {
		fence.EffectivePrivacy = "cloud_allowed"
	}
	keyDigest, requestDigest, body, err := s.submissionEnvelopePayload(key, submissionEnvelope{Version: 1, Request: r, Resume: &fence})
	if err != nil {
		return submissions.Status{}, err
	}
	status, err := db.CreateResumeSubmission(ctx, keyDigest, requestDigest, s.submissionConfigDigest(), body)
	return status, submissionError(err)
}

// SubmitBranch admits a new direct child of an exact, already-completed task
// head. Unlike generic continuations, a branch never waits for or recovers a
// source: the supplied content-free fence must match replayed durable history.
func (s *Service) SubmitBranch(ctx context.Context, key string, source sessions.TaskHeadFence, r Request) (submissions.Status, error) {
	if source.Validate() != nil || r.Compaction != nil || r.SummaryAttemptID != "" || r.ContinueTaskID != "" && r.ContinueTaskID != source.TaskID {
		return submissions.Status{}, ErrAdmission
	}
	r.ContinueTaskID = source.TaskID
	// Validate the complete public intent, including key, secret redaction and
	// request bounds, before opening or creating storage. The authoritative
	// branch envelope is assembled only after its source has been replayed.
	if _, _, _, err := s.submissionPayload(key, r); err != nil {
		return submissions.Status{}, err
	}
	db, err := telemetry.Open(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return submissions.Status{}, ErrSubmission
	}
	defer db.Close()
	fence, err := db.BranchSource(ctx, source.TaskID)
	if err != nil {
		return submissions.Status{}, submissionError(err)
	}
	if fence.TaskID != source.TaskID || fence.SessionID != source.SessionID || fence.HeadSequence != source.HeadSequence || fence.HeadEventID != source.HeadEventID {
		return submissions.Status{}, ErrAdmission
	}
	if fence.SourcePrivacy != "cloud_allowed" || r.LocalRequired {
		fence.EffectivePrivacy = "local_only"
	} else {
		fence.EffectivePrivacy = "cloud_allowed"
	}
	keyDigest, requestDigest, body, err := s.submissionEnvelopePayload(key, submissionEnvelope{Version: 1, Request: r, Branch: &fence})
	if err != nil {
		return submissions.Status{}, err
	}
	status, err := db.CreateBranchSubmission(ctx, keyDigest, requestDigest, s.submissionConfigDigest(), body)
	return status, submissionError(err)
}

func (s *Service) Submit(ctx context.Context, key string, r Request) (submissions.Status, error) {
	keyDigest, requestDigest, body, err := s.submissionPayload(key, r)
	if err != nil {
		return submissions.Status{}, err
	}
	db, err := telemetry.Open(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return submissions.Status{}, ErrSubmission
	}
	defer db.Close()
	status, err := db.CreateSubmission(ctx, keyDigest, requestDigest, s.submissionConfigDigest(), body)
	return status, submissionError(err)
}

// ResumeSubmission authorizes a reconnect against an existing exact
// idempotent request. Unlike Submit, it can never create or dispatch work.
func (s *Service) ResumeSubmission(ctx context.Context, key string, r Request) (submissions.Status, error) {
	keyDigest, requestDigest, _, err := s.submissionPayload(key, r)
	if err != nil {
		return submissions.Status{}, err
	}
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return submissions.Status{}, sql.ErrNoRows
		}
		return submissions.Status{}, ErrSubmission
	}
	defer db.Close()
	status, err := db.SubmissionByKey(ctx, keyDigest, requestDigest, s.submissionConfigDigest())
	return status, submissionError(err)
}

// ExistingFollowUpSubmission reconciles an acknowledgement-lost branch/resume
// without creating work. It derives the same exact source-bound envelope used by
// admission, so a pending browser operation can distinguish a committed child
// from a different task that merely advanced the chat head.
func (s *Service) ExistingFollowUpSubmission(ctx context.Context, key string, source sessions.TaskHeadFence, recovered bool, r Request) (submissions.Status, error) {
	if source.Validate() != nil || strings.TrimSpace(r.Prompt) == "" || len(r.Messages) != 0 || r.ContinueTaskID != "" || r.Compaction != nil || r.SummaryAttemptID != "" {
		return submissions.Status{}, ErrAdmission
	}
	r.ContinueTaskID = source.TaskID
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return submissions.Status{}, sql.ErrNoRows
		}
		return submissions.Status{}, ErrSubmission
	}
	defer db.Close()
	var envelope submissionEnvelope
	if recovered {
		fence, readErr := db.ResumeSource(ctx, source.TaskID)
		if readErr != nil {
			return submissions.Status{}, submissionError(readErr)
		}
		if fence.SessionID != source.SessionID || fence.HeadSequence != source.HeadSequence || fence.HeadEventID != source.HeadEventID {
			return submissions.Status{}, ErrAdmission
		}
		if fence.SourcePrivacy != "cloud_allowed" || r.LocalRequired {
			fence.EffectivePrivacy = "local_only"
		} else {
			fence.EffectivePrivacy = "cloud_allowed"
		}
		envelope = submissionEnvelope{Version: 1, Request: r, Resume: &fence}
	} else {
		fence, readErr := db.BranchSource(ctx, source.TaskID)
		if readErr != nil {
			return submissions.Status{}, submissionError(readErr)
		}
		if fence.SessionID != source.SessionID || fence.HeadSequence != source.HeadSequence || fence.HeadEventID != source.HeadEventID {
			return submissions.Status{}, ErrAdmission
		}
		if fence.SourcePrivacy != "cloud_allowed" || r.LocalRequired {
			fence.EffectivePrivacy = "local_only"
		} else {
			fence.EffectivePrivacy = "cloud_allowed"
		}
		envelope = submissionEnvelope{Version: 1, Request: r, Branch: &fence}
	}
	keyDigest, requestDigest, _, err := s.submissionEnvelopePayload(key, envelope)
	if err != nil {
		return submissions.Status{}, err
	}
	status, err := db.SubmissionByKey(ctx, keyDigest, requestDigest, s.submissionConfigDigest())
	return status, submissionError(err)
}

// RunSubmission durably admits one idempotent request and waits for its
// detached execution to become terminal. Canceling the caller stops only the
// wait: the submission remains owned by the daemon dispatcher and can be
// inspected or awaited again with the same key and request.
func (s *Service) RunSubmission(ctx context.Context, key string, r Request) (submissions.Status, error) {
	status, err := s.Submit(ctx, key, r)
	if err != nil || status.State != "queued" && status.State != "running" {
		return status, err
	}
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return status, submissionError(err)
	}
	defer db.Close()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-ticker.C:
		}
		status, err = db.Submission(ctx, status.ID)
		if err != nil || status.State != "queued" && status.State != "running" {
			return status, submissionError(err)
		}
	}
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
	envelope, err := decodeSubmissionEnvelope(body)
	if err != nil {
		return Request{}, err
	}
	return envelope.Request, nil
}

func decodeSubmissionEnvelope(body []byte) (submissionEnvelope, error) {
	var envelope submissionEnvelope
	if len(body) > 8<<20 {
		return submissionEnvelope{}, ErrAdmission
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF || envelope.Version != 1 || validateInput(envelope.Request) != nil || envelope.Branch != nil && envelope.Branch.Validate() != nil || envelope.Resume != nil && envelope.Resume.Validate() != nil || envelope.Branch != nil && envelope.Resume != nil {
		return submissionEnvelope{}, ErrAdmission
	}
	// Only the intake's canonical representation is accepted: this additionally
	// rejects duplicate keys and differently cased aliases accepted by encoding/json.
	canonical, err := json.Marshal(envelope)
	if err != nil || !bytes.Equal(body, canonical) {
		return submissionEnvelope{}, ErrAdmission
	}
	if envelope.Branch != nil && (envelope.Request.ContinueTaskID != envelope.Branch.TaskID || envelope.Request.Compaction != nil || envelope.Request.SummaryAttemptID != "") {
		return submissionEnvelope{}, ErrAdmission
	}
	if envelope.Resume != nil && (envelope.Request.ContinueTaskID != envelope.Resume.TaskID || strings.TrimSpace(envelope.Request.Prompt) == "" || len(envelope.Request.Messages) != 0 || envelope.Request.Compaction != nil || envelope.Request.SummaryAttemptID != "") {
		return submissionEnvelope{}, ErrAdmission
	}
	return envelope, nil
}
