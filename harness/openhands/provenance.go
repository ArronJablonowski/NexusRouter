package openhands

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/ArronJablonowski/NexusRouter/harness"
)

const AdapterVersion = "openhands-sdk-text-v1"

// Identity binds the admitted policy/configuration and pinned native binary. Revision
// is a trusted host attestation; a model name in the stream cannot prove weights.
// Credentials and ephemeral paths/tokens never enter the learning identity.
func (c Config) Identity() (harness.Identity, error) {
	if c.validate() != nil {
		return harness.Identity{}, ErrProjection
	}
	bridgeHash := sha256.Sum256([]byte(bridgeSource))
	configuration := struct {
		Version                                                       int
		Artifact, Runtime, Bridge, Endpoint, Policy, Protocol, Prompt string
		Context, Output                                               int
		DeadlineNanos                                                 int64
		Prices                                                        Prices
	}{1, c.ExecutableSHA256, c.RuntimeSHA256, hex.EncodeToString(bridgeHash[:]), c.BaseURL, c.TransportPolicySHA256, c.UpstreamProtocol, systemPrompt, c.ContextTokens, c.MaxOutputTokens, int64(c.Timeout), *c.Prices}
	body, err := json.Marshal(configuration)
	if err != nil {
		return harness.Identity{}, ErrProjection
	}
	hash := sha256.Sum256(body)
	identity := harness.Identity{Version: harness.Version, Harness: "openhands", HarnessVersion: SupportedVersion, AdapterVersion: AdapterVersion, Provider: c.Provider, Model: c.Model, ModelRevision: c.ModelRevision, ConfigSHA256: hex.EncodeToString(hash[:])}
	if identity.Validate() != nil {
		return harness.Identity{}, ErrProjection
	}
	return identity, nil
}
