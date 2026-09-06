package app

import (
	"context"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/memory"
)

// ExportMemory exports the configured scope without inference or mutation.
// Optional custom exporters must guarantee one complete consistent observation;
// the service cannot manufacture this guarantee by collecting live pages.
func (s *Service) ExportMemory(ctx context.Context) (memory.ExportSnapshot, error) {
	var out memory.ExportSnapshot
	err := s.memoryManagement(ctx, false, func(ctx context.Context, store memory.Store, scope string, secrets []string) error {
		exporter, ok := store.(memory.Exporter)
		if !ok {
			return ErrAdmission
		}
		now := time.Now().UTC()
		snapshot, err := exporter.ExportMemory(ctx, scope, now)
		if err != nil || snapshot.Scope != scope || !snapshot.CapturedAt.Equal(now) || snapshot.Validate() != nil {
			return ErrAdmission
		}
		secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
		if !memoryManagementKey(scope, secrets) {
			return ErrAdmission
		}
		// A custom store may retain its slice. Never redact it in place.
		out = snapshot
		out.Facts = make([]memory.Fact, len(snapshot.Facts))
		for i, f := range snapshot.Facts {
			if ctx.Err() != nil {
				return ErrAdmission
			}
			clean, err := memoryManagementFact(f, scope, secrets)
			if err != nil {
				return ErrAdmission
			}
			out.Facts[i] = clean
		}
		return out.Validate()
	})
	if err != nil {
		return memory.ExportSnapshot{}, ErrAdmission
	}
	return out, nil
}
