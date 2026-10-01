package cli

import (
	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

// The operator process owns this handle. Attach before starting requests, and
// close after its workers and HTTP handlers stop. SDK stores remain caller-owned.
func attachNativeHarnessEvidence(s config.Settings, service *app.Service) (func() error, error) {
	if s.NativeHarnessEvidenceDir == "" {
		return func() error { return nil }, nil
	}
	ledger, err := harness.OpenEvidenceStore(s.NativeHarnessEvidenceDir)
	if err != nil {
		return nil, err
	}
	service.ConfigureHarnessEvidence(ledger)
	return ledger.Close, nil
}
