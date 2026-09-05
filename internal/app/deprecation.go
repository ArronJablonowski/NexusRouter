package app

import (
	"context"
	"errors"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/memory"
	"github.com/ArronJablonowski/DarwinRouter/routing"
)

var ErrDeprecation = errors.New("model deprecation report unavailable")

// ModelDeprecation is an operator inspection, never a model lifecycle action.
// Configured memory estimates are potential residency savings, not measured
// freed memory, disk savings, or an assertion that a model is currently loaded.
func (s *Service) ModelDeprecation(ctx context.Context, modelID, domain, profile string, policy evaluation.DeprecationPolicy) (out evaluation.DeprecationReport, resultErr error) {
	defer func() {
		if recover() != nil {
			out = evaluation.DeprecationReport{}
			resultErr = ErrDeprecation
		}
	}()
	bad := func() (evaluation.DeprecationReport, error) { return evaluation.DeprecationReport{}, ErrDeprecation }
	if s == nil || ctx == nil || ctx.Err() != nil || !memory.ValidKey(modelID) || !memory.ValidKey(domain) || !memory.ValidKey(profile) || policy.Validate() != nil {
		return bad()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, model := range s.settings.Models {
		if model.ID != modelID {
			continue
		}
		db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
		if err != nil {
			return bad()
		}
		defer db.Close()
		key := routing.Key{Model: model.Model, Provider: model.Provider, Domain: domain, Profile: profile}
		records, err := db.DeprecationEvidence(ctx, key, policy.Window)
		if err != nil {
			return bad()
		}
		report, err := evaluation.SummarizeDeprecation(records, policy)
		if err != nil || ctx.Err() != nil {
			return bad()
		}
		// Empty populations retain exact requested attribution without inventing
		// measured success or failure. Never expose credential-bearing IDs.
		report.Key = key
		secrets := memorySecrets(s.settings, s.secret)
		report.Key.Model = redact(report.Key.Model, secrets)
		report.Key.Provider = redact(report.Key.Provider, secrets)
		report.Key.Domain = redact(report.Key.Domain, secrets)
		report.Key.Profile = redact(report.Key.Profile, secrets)
		report.ConfiguredModelID = redact(model.ID, secrets)
		if model.Locality == "local" {
			report.EstimatedRAMBytes, report.EstimatedVRAMBytes = model.RAMBytes, model.VRAMBytes
		}
		return report, nil
	}
	return bad()
}
