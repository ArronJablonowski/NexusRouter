package remotecli

import (
	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"os"
)

func remoteSDKOptions(file string, cfg config.Settings, ledger *harness.EvidenceStore) sdk.ConfigOptions {
	options := sdk.ConfigOptions{ProjectFile: file, LookupSecret: os.Getenv, HarnessEvidence: ledger}
	// The SDK client here reads history/identity; task intake is owned by the
	// separately configured application service. Text-only remote execution has
	// no local ComfyUI approval presenter. Keep that SDK view free of ComfyUI
	// execution authority instead of weakening its mandatory approval guard.
	if cfg.ComfyUI.Enabled {
		options.Overrides = map[string]string{"comfyui.enabled": "false"}
	}
	return options
}
