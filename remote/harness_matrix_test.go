package remote

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

// The optional operator-owned fixture file supplies installed, pinned runtime
// registrations only. No production config, provider or model is used. Each
// case creates its own synthetic provider, journal, ledger and isolated sshd.
func TestRemoteSDKAllNativeHarnessesSSH(t *testing.T) {
	path := os.Getenv("NEXUS_REMOTE_HARNESS_FIXTURES")
	if path == "" || os.Getenv("NEXUS_REMOTE_SSH_NATIVE") != "1" {
		t.Skip("requires pinned harness fixture registrations and native SSH")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, MaxBody+1))
	if err != nil || len(body) > MaxBody {
		t.Fatal("invalid fixture file", err)
	}
	var registrations []config.NativeHarness
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&registrations); err != nil {
		t.Fatal(err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		t.Fatal("trailing fixture input")
	}
	if len(registrations) != 5 {
		t.Fatal("qualification requires all five native harnesses")
	}
	seen := map[string]bool{}
	for _, h := range registrations {
		switch h.Kind {
		case "pi", "goose", "openclaw", "openhands", "hermes":
		default:
			t.Fatal("unknown harness")
		}
		if seen[h.Kind] || h.ModelID != "chat" || h.ModelRevision != "fixture-v1" || h.NativeTools {
			t.Fatal("invalid fixture registration")
		}
		seen[h.Kind] = true
	}
	for _, h := range registrations {
		t.Run(h.Kind, func(t *testing.T) { remoteSDKLifecycle(t, true, true, h) })
	}
}
