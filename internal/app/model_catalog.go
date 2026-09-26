package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/routing"
)

// ConfiguredModelCatalog returns owned declared routing metadata. It performs
// no inference, health check, reservation, or task storage read. Cloud context
// recommendations are refreshed from the provider-owned local catalog.
func (s *Service) ConfiguredModelCatalog(ctx context.Context) (routing.ModelCatalog, error) {
	zero := routing.ModelCatalog{}
	if ctx == nil {
		return zero, ErrAdmission
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	settings := config.WithCloudContextRecommendations(s.settings)
	secrets := memorySecrets(settings, s.secret)
	configID, err := settingsConfigID(settings)
	if err != nil {
		return zero, ErrAdmission
	}
	catalog := routing.ModelCatalog{Version: 1, ConfigID: configID, Models: make([]routing.ConfiguredModel, len(settings.Models))}
	for i, configured := range settings.Models {
		var cost *float64
		if configured.EstimatedCost != nil {
			value := *configured.EstimatedCost
			cost = &value
		}
		catalog.Models[i] = routing.ConfiguredModel{
			Version: 1, ID: configured.ID, Provider: configured.Provider,
			Model: configured.Model, ReasoningEffort: configured.ReasoningEffort, Locality: configured.Locality,
			Capabilities:  append([]string(nil), configured.Capabilities...),
			ContextTokens: configured.ContextTokens, EstimatedCost: cost,
			RAMBytes: configured.RAMBytes, VRAMBytes: configured.VRAMBytes,
			GPUDevice: configured.GPUDevice, FailureDomain: configured.FailureDomain,
		}
	}
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if ctx.Err() != nil || catalog.Validate() != nil || !selectionValueClean(catalog, secrets) {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		return zero, ErrAdmission
	}
	return catalog, nil
}

func settingsConfigID(settings config.Settings) (string, error) {
	redacted, err := settings.RedactedJSON()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(redacted)
	return hex.EncodeToString(digest[:]), nil
}
