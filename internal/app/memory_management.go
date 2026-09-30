package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/memory"
)

// Operator management remains available when retrieval is disabled. Its scope
// is always configured, never supplied by an inference result or route.
func (s *Service) memoryManagement(ctx context.Context, write bool, fn func(context.Context, memory.Store, string, []string) error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrAdmission
		}
	}()
	if s == nil || ctx == nil || ctx.Err() != nil || !memory.ValidKey(s.settings.Memory.Scope) {
		return ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	secrets := memorySecrets(s.settings, s.secret)
	scope := s.settings.Memory.Scope
	if !memoryManagementKey(scope, secrets) {
		return ErrAdmission
	}
	store := s.memoryStore
	if store == nil {
		var db *telemetry.Store
		if write {
			db, err = telemetry.OpenMemoryControl(ctx, s.settings.Telemetry.Database)
		} else {
			db, err = telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
		}
		if err != nil {
			return ErrAdmission
		}
		defer db.Close()
		store = db
	}
	operationErr := fn(ctx, store, scope, secrets)
	if ctx.Err() != nil {
		return ErrAdmission
	}
	if errors.Is(operationErr, memory.ErrConflict) {
		return memory.ErrConflict
	}
	if operationErr != nil {
		return ErrAdmission
	}
	return nil
}

func memoryManagementKey(value string, secrets []string) bool {
	return memory.ValidKey(value) && utf8.ValidString(value) && redact(value, secrets) == value
}
func memoryManagementFact(f memory.Fact, scope string, secrets []string) (memory.Fact, error) {
	if f.Validate() != nil || f.Scope != scope || !memoryManagementKey(scope, secrets) || !memoryManagementKey(f.ID, secrets) || !utf8.ValidString(f.Content) || !utf8.ValidString(f.Provenance) {
		return memory.Fact{}, ErrAdmission
	}
	f.Content, f.Provenance = redact(f.Content, secrets), redact(f.Provenance, secrets)
	if f.Validate() != nil {
		return memory.Fact{}, ErrAdmission
	}
	return f, nil
}

func (s *Service) Memory(ctx context.Context, id string) (memory.Fact, error) {
	if !memory.ValidKey(id) {
		return memory.Fact{}, ErrAdmission
	}
	var out memory.Fact
	err := s.memoryManagement(ctx, false, func(ctx context.Context, store memory.Store, scope string, secrets []string) error {
		if !memoryManagementKey(id, secrets) {
			return ErrAdmission
		}
		f, err := store.GetMemory(ctx, scope, id)
		if err != nil {
			return err
		}
		if f.ID != id {
			return ErrAdmission
		}
		// A trusted backend may outlive credential rotation. Retain the admission
		// credentials as well as fresh observations before releasing its result.
		secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
		out, err = memoryManagementFact(f, scope, secrets)
		return err
	})
	if err != nil {
		return memory.Fact{}, err
	}
	return out, nil
}

func (s *Service) Memories(ctx context.Context, after, contains string, limit int, includeExpired bool) ([]memory.Fact, error) {
	if (memory.Query{Scope: "validation", AfterID: after, Contains: contains, Limit: limit, Now: time.Now().UTC()}).Validate() != nil || limit > 100 {
		return nil, ErrAdmission
	}
	out := []memory.Fact{}
	err := s.memoryManagement(ctx, false, func(ctx context.Context, store memory.Store, scope string, secrets []string) error {
		if limit < 1 || limit > 100 || (after != "" && !memoryManagementKey(after, secrets)) || !utf8.ValidString(contains) || redact(contains, secrets) != contains {
			return ErrAdmission
		}
		now := time.Now().UTC()
		q := memory.Query{Scope: scope, AfterID: after, Contains: contains, Limit: limit, IncludeExpired: includeExpired, LocalOnly: true, Now: now}
		if q.Validate() != nil {
			return ErrAdmission
		}
		facts, err := store.QueryMemory(ctx, q)
		if err != nil || len(facts) > limit {
			return ErrAdmission
		}
		secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
		if !memoryManagementKey(scope, secrets) || (after != "" && !memoryManagementKey(after, secrets)) || redact(contains, secrets) != contains {
			return ErrAdmission
		}
		last := after
		budget := (8 << 20) - 2 // JSON array delimiters; include escaped string bytes.
		for _, f := range facts {
			if f.ID <= last || !strings.Contains(f.Content, contains) || (!includeExpired && !f.Expires.IsZero() && !f.Expires.After(now)) {
				return ErrAdmission
			}
			clean, err := memoryManagementFact(f, scope, secrets)
			if err != nil {
				return ErrAdmission
			}
			encoded, err := json.Marshal(clean)
			budget -= len(encoded) + 1
			if err != nil || budget < 0 {
				return ErrAdmission
			}
			out = append(out, clean)
			last = f.ID
		}
		return nil
	})
	if err != nil {
		return nil, ErrAdmission
	}
	return out, nil
}

func (s *Service) PutMemory(ctx context.Context, fact memory.Fact, expected int64) error {
	if fact.Validate() != nil || expected < 0 || fact.Revision-1 != expected {
		return ErrAdmission
	}
	return s.memoryManagement(ctx, true, func(ctx context.Context, store memory.Store, scope string, secrets []string) error {
		if expected < 0 || fact.Revision-1 != expected {
			return ErrAdmission
		}
		clean, err := memoryManagementFact(fact, scope, secrets)
		if err != nil {
			return err
		}
		return store.PutMemory(ctx, clean, expected)
	})
}

func (s *Service) DeleteMemory(ctx context.Context, id string, expected int64) error {
	if !memory.ValidKey(id) || expected < 1 {
		return ErrAdmission
	}
	return s.memoryManagement(ctx, true, func(ctx context.Context, store memory.Store, scope string, secrets []string) error {
		if expected < 1 || !memoryManagementKey(id, secrets) {
			return ErrAdmission
		}
		return store.DeleteMemory(ctx, scope, id, expected)
	})
}
