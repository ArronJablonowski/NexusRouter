package app

import (
	"context"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

// checkOutcomeSelectionSources validates only the saved membership. It neither
// selects another window nor updates the report. Its read-only SQLite snapshot
// is not a distributed transaction with the later catalog replacement.
func (s *Service) checkOutcomeSelectionSources(ctx context.Context, database string, report skills.ComparisonSelectionReport, priorSecrets []string) ([]string, error) {
	if ctx == nil || ctx.Err() != nil || report.Validate() != nil || report.Sources == nil {
		return nil, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	secrets := append(append([]string(nil), priorSecrets...), memorySecrets(s.settings, s.secret)...)
	if s.settings.Telemetry.Database != database || !selectionValueClean(report, secrets) {
		return nil, ErrAdmission
	}
	db, err := telemetry.OpenReadOnly(ctx, database)
	if err != nil {
		return nil, ErrAdmission
	}
	defer db.Close()
	observations, err := db.CheckSkillComparisonSources(ctx, report)
	if err != nil || ctx.Err() != nil {
		return nil, ErrAdmission
	}
	for _, observation := range observations {
		if observation.Validate() != nil || observation.SkillContext == nil || !observation.SkillContext.Complete || observation.Privacy != report.Policy.Privacy {
			return nil, ErrAdmission
		}
		for _, ref := range observation.SkillContext.References {
			if ref.Scope != report.Policy.Comparison.Key.Scope {
				return nil, ErrAdmission
			}
		}
	}
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if ctx.Err() != nil || s.settings.Telemetry.Database != database || !selectionValueClean([]any{report, observations}, secrets) {
		return nil, ErrAdmission
	}
	return secrets, nil
}
