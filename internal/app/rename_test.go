package app

import (
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

func TestRenameMemoryRedactsBothAPITokenNames(t *testing.T) {
	secrets := map[string]string{"NEXUS_API_TOKEN": "canonical-secret", "DARWIN_API_TOKEN": "legacy-secret"}
	values := memorySecrets(config.Settings{}, func(name string) string { return secrets[name] })
	found := map[string]bool{}
	for _, value := range values {
		found[value] = true
	}
	if !found["canonical-secret"] || !found["legacy-secret"] {
		t.Fatal("both credential names must be protected")
	}
}
