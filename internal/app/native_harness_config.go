package app

import (
	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

func configuredNativeHarnesses(settings config.Settings) []NativeHarness {
	entries := make([]NativeHarness, 0, len(settings.NativeHarnesses))
	for _, h := range settings.NativeHarnesses {
		entry := NativeHarness{ID: h.ID, Kind: h.Kind, ModelID: h.ModelID, Executable: h.Executable, ExecutableSHA256: h.ExecutableSHA256, ModelRevision: h.ModelRevision, RuntimeSHA256: h.RuntimeSHA256, HermesSourceDir: h.HermesSourceDir, MaxOutputTokens: h.MaxOutputTokens, OverheadRAMBytes: h.OverheadRAMBytes}
		if h.Prices != nil {
			entry.Prices = &NativeHarnessPrices{Input: h.Prices.Input, Output: h.Prices.Output, CacheRead: h.Prices.CacheRead, CacheWrite: h.Prices.CacheWrite}
		}
		entries = append(entries, entry)
	}
	return entries
}

// ConfigureHarnessEvidence is constructor-only and preserves configured registrations.
func (s *Service) ConfigureHarnessEvidence(ledger *harness.EvidenceStore) { s.harnessEvidence = ledger }
