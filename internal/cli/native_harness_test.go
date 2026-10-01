package cli

import "testing"

func TestRunParsesNativeHarnessSelection(t *testing.T) {
	_, req, err := parseRunArgs([]string{"--config", "fixture.yaml", "--model", "chat", "--harness", "pi-local"})
	if err != nil || req.HarnessID != "pi-local" || req.ModelID != "chat" {
		t.Fatal(req, err)
	}
}
