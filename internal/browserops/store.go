// Package browserops presents the browser operation journal without owning a
// second database. All rows live in the main telemetry SQLite/WAL database.
package browserops

import (
	"context"
	"errors"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
)

const (
	MaxRequestBytes  = telemetry.MaxBrowserOperationRequestBytes
	MaxResponseBytes = telemetry.MaxBrowserOperationResponseBytes
)

var (
	ErrConflict    = telemetry.ErrBrowserOperationConflict
	ErrInvalid     = telemetry.ErrBrowserOperationInvalid
	ErrCapacity    = telemetry.ErrBrowserOperationCapacity
	ErrUnavailable = errors.New("browser operation store unavailable")
)

type Record = telemetry.BrowserOperation
type Recovery = telemetry.BrowserOperationRecovery

type Store struct{ telemetry *telemetry.Store }

func Open(ctx context.Context, path string) (*Store, error) {
	store, err := telemetry.Open(ctx, path)
	if err != nil {
		return nil, ErrUnavailable
	}
	return &Store{telemetry: store}, nil
}

func (s *Store) Close() error {
	if s == nil || s.telemetry == nil {
		return nil
	}
	return s.telemetry.Close()
}

func (s *Store) Begin(ctx context.Context, subject, key, kind string, request []byte) (Record, error) {
	if s == nil || s.telemetry == nil {
		return Record{}, ErrInvalid
	}
	return s.telemetry.BeginBrowserOperation(ctx, subject, key, kind, request)
}

func (s *Store) Commit(ctx context.Context, subject, operationID, requestDigest string, response []byte) (Record, error) {
	if s == nil || s.telemetry == nil {
		return Record{}, ErrInvalid
	}
	return s.telemetry.CommitBrowserOperation(ctx, subject, operationID, requestDigest, response)
}

func (s *Store) Reject(ctx context.Context, subject, operationID, requestDigest string, response []byte) (Record, error) {
	if s == nil || s.telemetry == nil {
		return Record{}, ErrInvalid
	}
	return s.telemetry.RejectBrowserOperation(ctx, subject, operationID, requestDigest, response)
}

func (s *Store) Adoptable(ctx context.Context, recoverySubject, key, kind string, request []byte) (Record, bool, error) {
	if s == nil || s.telemetry == nil {
		return Record{}, false, ErrInvalid
	}
	return s.telemetry.AdoptableBrowserOperation(ctx, recoverySubject, key, kind, request)
}

func (s *Store) Recover(ctx context.Context, recoverySubject string, record Record, state string, response []byte) (Record, error) {
	if s == nil || s.telemetry == nil {
		return Record{}, ErrInvalid
	}
	return s.telemetry.RecoverBrowserOperation(ctx, recoverySubject, record, state, response)
}

func (s *Store) Recovery(ctx context.Context, operationID string) (Recovery, error) {
	if s == nil || s.telemetry == nil {
		return Recovery{}, ErrInvalid
	}
	return s.telemetry.BrowserOperationRecovery(ctx, operationID)
}

func (s *Store) List(ctx context.Context, subject, after string, limit int) ([]Record, string, error) {
	if s == nil || s.telemetry == nil {
		return nil, "", ErrInvalid
	}
	return s.telemetry.BrowserOperations(ctx, subject, after, limit)
}
