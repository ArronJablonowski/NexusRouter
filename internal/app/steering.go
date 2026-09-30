package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

var ErrSteeringControl = errors.New("task steering unavailable")

func (s *Service) ListSteering(ctx context.Context, task string) ([]runtime.SteeringMessage, error) {
	if !sessions.ValidEventPageID(task) {
		return nil, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := s.CancellationStatus(ctx, task); err != nil {
		return nil, err
	}
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return nil, ErrSteeringControl
	}
	defer db.Close()
	messages, err := db.ListSteering(ctx, task)
	return messages, steeringError(err)
}

// SteerTask durably queues user guidance. Acceptance is not execution: the
// runtime applies it at a safe boundary without changing tools/privacy/budgets.
func (s *Service) SteerTask(ctx context.Context, task, key, text string) (runtime.SteeringMessage, error) {
	return s.SteerTaskAtRevision(ctx, task, key, text, 0)
}

func (s *Service) SteerTaskAtRevision(ctx context.Context, task, key, text string, expected int64) (runtime.SteeringMessage, error) {
	if !sessions.ValidEventPageID(task) || len(key) == 0 || len(key) > 128 || !utf8.ValidString(key) || strings.TrimSpace(key) == "" || !runtime.ValidSteeringText(text) {
		return runtime.SteeringMessage{}, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Inspect before opening a writer, so unknown tasks never create a store.
	if _, err := s.CancellationStatus(ctx, task); err != nil {
		return runtime.SteeringMessage{}, err
	}
	text = redact(text, memorySecrets(s.settings, s.secret))
	if !runtime.ValidSteeringText(text) {
		return runtime.SteeringMessage{}, ErrAdmission
	}
	hash := sha256.Sum256([]byte(key))
	db, err := telemetry.Open(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return runtime.SteeringMessage{}, ErrSteeringControl
	}
	defer db.Close()
	message, err := db.QueueSteeringAtRevision(ctx, task, hex.EncodeToString(hash[:]), text, expected)
	return message, steeringError(err)
}

func (s *Service) SteeringStatus(ctx context.Context, task, id string) (runtime.SteeringMessage, error) {
	if !sessions.ValidEventPageID(task) || !sessions.ValidEventPageID(id) {
		return runtime.SteeringMessage{}, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := s.CancellationStatus(ctx, task); err != nil {
		return runtime.SteeringMessage{}, err
	}
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return runtime.SteeringMessage{}, ErrSteeringControl
	}
	defer db.Close()
	message, err := db.SteeringStatus(ctx, task, id)
	return message, steeringError(err)
}

func steeringError(err error) error {
	for _, known := range []error{sql.ErrNoRows, telemetry.ErrConflict, runtime.ErrSteeringClosed, runtime.ErrSteeringLimit, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, known) {
			return known
		}
	}
	if err != nil {
		return ErrSteeringControl
	}
	return nil
}
