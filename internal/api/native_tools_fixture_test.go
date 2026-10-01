package api

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"os"
	"os/exec"
	"testing"
)

func nativeHTTPRegistration(t *testing.T, kind string) config.NativeHarness {
	t.Helper()
	executable, e := exec.LookPath("pi")
	if kind == "goose" {
		executable = "/Users/aj_lobster/Documents/Codex/2026-09-19/do-x20/outputs/harness-runtime/goose-1.52.0/goose"
		e = nil
	}
	var runtimeDigest string
	if kind == "openhands" {
		executable = os.Getenv("NEXUS_OPENHANDS_PYTHON")
		var manifest []byte
		manifest, e = os.ReadFile(os.Getenv("NEXUS_OPENHANDS_MANIFEST"))
		sum := sha256.Sum256(manifest)
		runtimeDigest = hex.EncodeToString(sum[:])
	}
	if e != nil {
		t.Fatal(e)
	}
	artifact, e := os.ReadFile(executable)
	if e != nil {
		t.Fatal(e)
	}
	pin := sha256.Sum256(artifact)
	return config.NativeHarness{ID: "native-tools", NativeTools: true, Kind: kind, ModelID: "chat", Executable: executable, ExecutableSHA256: hex.EncodeToString(pin[:]), RuntimeSHA256: runtimeDigest, ModelRevision: "fixture-v1", MaxOutputTokens: 1024, OverheadRAMBytes: 64 << 20, Prices: &config.NativeHarnessPrices{}}
}
