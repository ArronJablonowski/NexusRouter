package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func nativeHTTPRegistration(t *testing.T, kind string) config.NativeHarness {
	t.Helper()
	executable, e := exec.LookPath("pi")
	if kind == "goose" {
		executable = "/Users/aj_lobster/Documents/Codex/2026-09-19/do-x20/outputs/harness-runtime/goose-1.52.0/goose"
		e = nil
	}
	if kind == "openclaw" {
		executable, e = exec.LookPath("openclaw")
	}
	var runtimeDigest, source string
	if kind == "hermes" {
		source = "/Users/aj_lobster/.hermes/hermes-agent"
		sum := sha256.Sum256([]byte(source))
		key := hex.EncodeToString(sum[:])[:16]
		facts, err := os.ReadFile(filepath.Join("/Users/aj_lobster/.hermes/installs", key, "facts.json"))
		if err != nil {
			t.Fatal(err)
		}
		var state struct {
			Packages struct{ Venv struct{ Environment string } }
		}
		if json.Unmarshal(facts, &state) != nil || state.Packages.Venv.Environment == "" {
			t.Fatal("missing Hermes runtime")
		}
		executable = filepath.Join(state.Packages.Venv.Environment, "bin", "python")
		digest := sha256.Sum256(facts)
		runtimeDigest = hex.EncodeToString(digest[:])
		e = nil
	}
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
	return config.NativeHarness{ID: "native-tools", NativeTools: true, Kind: kind, ModelID: "chat", Executable: executable, ExecutableSHA256: hex.EncodeToString(pin[:]), RuntimeSHA256: runtimeDigest, HermesSourceDir: source, ModelRevision: "fixture-v1", MaxOutputTokens: 1024, OverheadRAMBytes: 64 << 20, Prices: &config.NativeHarnessPrices{}}
}
