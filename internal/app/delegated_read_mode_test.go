package app

import (
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
)

func TestDelegatedReadModeRequiresExactOperatorAuthority(t *testing.T) {
	base := config.Defaults()
	base.Tools.Enabled = true
	base.Tools.ReadRoot = t.TempDir()
	base.Workers.DelegateModel = "local-worker"
	base.Workers.DelegateReadTools = true
	for _, test := range []struct {
		name    string
		change  func(*config.Settings)
		allowed bool
	}{
		{name: "both toggles enabled", change: func(*config.Settings) {}, allowed: true},
		{name: "file tools disabled", change: func(settings *config.Settings) { settings.Tools.Enabled = false }, allowed: false},
		{name: "delegated reads disabled", change: func(settings *config.Settings) { settings.Workers.DelegateReadTools = false }, allowed: false},
		{name: "worker absent", change: func(settings *config.Settings) { settings.Workers.DelegateModel = "" }, allowed: false},
		{name: "create authority present", change: func(settings *config.Settings) { settings.Tools.CreateEnabled = true }, allowed: false},
		{name: "replace authority present", change: func(settings *config.Settings) { settings.Tools.ReplaceEnabled = true }, allowed: false},
		{name: "workboard read present", change: func(settings *config.Settings) { settings.Tools.WorkboardReadEnabled = true }, allowed: false},
		{name: "workboard write present", change: func(settings *config.Settings) { settings.Tools.WorkboardWriteEnabled = true }, allowed: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings := base
			test.change(&settings)
			if delegatedReadsConfigured(settings) != test.allowed {
				t.Fatalf("delegated authority mismatch: %+v", settings.Tools)
			}
		})
	}
}

func TestCloudDelegatedReadModeRejectsDirectOrChildUse(t *testing.T) {
	settings := config.Defaults()
	settings.Tools.Enabled = true
	settings.Tools.ReadRoot = t.TempDir()
	settings.Workers.DelegateModel = "local-worker"
	settings.Workers.DelegateReadTools = true
	cloud := config.Model{ID: "coordinator", Locality: "cloud"}
	if !cloudDelegatedReads(settings, Request{}, cloud) {
		t.Fatal("narrow cloud delegation mode was not admitted")
	}
	if cloudDelegatedReads(settings, Request{delegatedParent: "parent"}, cloud) {
		t.Fatal("delegated child inherited cloud delegation mode")
	}
	if cloudDelegatedReads(settings, Request{}, config.Model{ID: "worker", Locality: "local"}) {
		t.Fatal("local model misclassified as cloud coordinator")
	}
}
