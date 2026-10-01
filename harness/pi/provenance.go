package pi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"

	"github.com/ArronJablonowski/NexusRouter/harness"
)

const AdapterVersion = "pi-rpc-text-v4"

// Identity binds the actual protocol-checked provider/model to the admitted
// configuration. ModelRevision is a trusted host attestation of the deployed
// weights/revision; Pi's model name alone cannot establish it. The caller must
// verify that attestation before admission. Credentials are excluded entirely.
func (c Config) Identity() (harness.Identity, error) {
	if c.validate() != nil {
		return harness.Identity{}, ErrProtocol
	}
	effective := struct {
		Version                                                             int
		Artifact, Endpoint, SystemPrompt, TransportPolicy, UpstreamProtocol string
		Context, Output                                                     int
		DeadlineNanos                                                       int64
		Prices                                                              Prices
	}{1, c.ExecutableSHA256, c.BaseURL, systemPrompt, c.TransportPolicySHA256, c.UpstreamProtocol, c.ContextTokens, c.MaxOutputTokens, int64(c.Timeout), *c.Prices}
	body, err := json.Marshal(effective)
	if err != nil {
		return harness.Identity{}, ErrProtocol
	}
	sum := sha256.Sum256(body)
	id := harness.Identity{Version: harness.Version, Harness: "pi", HarnessVersion: SupportedVersion, AdapterVersion: AdapterVersion, Provider: c.Provider, Model: c.Model, ModelRevision: c.ModelRevision, ConfigSHA256: hex.EncodeToString(sum[:])}
	if id.Validate() != nil {
		return harness.Identity{}, ErrProtocol
	}
	return id, nil
}

// Usage is reported by Pi, which can normalize unavailable provider counts to
// zero. These values must not be relabeled provider-measured or treated as proof
// of zero cost. Reasoning is included in Output; CacheWrite1h in CacheWrite.
type Usage struct {
	Input, Output, CacheRead, CacheWrite, TotalTokens *int64
	CacheWrite1h, Reasoning                           *int64
	Cost                                              *UsageCost
}
type UsageCost struct{ Input, Output, CacheRead, CacheWrite, Total float64 }

func (u *Usage) valid() bool {
	if u == nil || u.Input == nil || u.Output == nil || u.CacheRead == nil || u.CacheWrite == nil || u.TotalTokens == nil || u.Cost == nil {
		return false
	}
	sum := int64(0)
	for _, n := range []*int64{u.Input, u.Output, u.CacheRead, u.CacheWrite} {
		if *n < 0 || *n > 1<<40 {
			return false
		}
		sum += *n
	}
	if *u.TotalTokens != sum {
		return false
	}
	if u.Reasoning != nil && (*u.Reasoning < 0 || *u.Reasoning > *u.Output) {
		return false
	}
	if u.CacheWrite1h != nil && (*u.CacheWrite1h < 0 || *u.CacheWrite1h > *u.CacheWrite) {
		return false
	}
	for _, n := range []float64{u.Cost.Input, u.Cost.Output, u.Cost.CacheRead, u.Cost.CacheWrite, u.Cost.Total} {
		if !finiteNonnegative(n) {
			return false
		}
	}
	return true
}

func finiteNonnegative(v float64) bool { return v >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }
