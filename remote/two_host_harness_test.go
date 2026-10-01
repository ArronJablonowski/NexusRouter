package remote

import (
	"bytes"
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"io"
	"os"
	"testing"
)

// Optional pinned destination registration; never installs or updates a harness.
func physicalHarnessRegistration(t *testing.T) *config.NativeHarness {
	t.Helper()
	path := os.Getenv("NEXUS_REMOTE_TEST_HARNESS_FIXTURE")
	if path == "" {
		return nil
	}
	body, e := os.ReadFile(path)
	if e != nil || len(body) > MaxBody {
		t.Fatal("invalid physical harness fixture", e)
	}
	var h config.NativeHarness
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(&h); e != nil {
		t.Fatal(e)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || (h.Kind != "pi" && h.Kind != "openclaw" && h.Kind != "goose") || h.ModelID != "chat" || h.ModelRevision != "fixture-v1" || h.NativeTools {
		t.Fatal("unsupported physical harness fixture")
	}
	return &h
}
