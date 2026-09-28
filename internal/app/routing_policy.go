package app

import (
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/routing"
	"time"
)

// configuredRoutingPolicy is shared by dispatch and read-only ranking previews.
func configuredRoutingPolicy(cfg config.Settings) (routing.Policy, routing.DecayResolver) {
	p := routing.Defaults()
	p.MinSamples, p.Exploration = cfg.Routing.MinSamples, cfg.Routing.Exploration
	p.HalfLife, _ = config.Duration(cfg.Routing.HalfLife)
	w := cfg.Routing.Weights
	p.Weights = routing.Weights{Quality: w["quality"], Compliance: w["schema_compliance"], Reliability: w["reliability"], Latency: w["latency"], Cost: w["cost"], Recency: w["recency"], Uncertainty: w["uncertainty"]}
	return p, routing.DecayResolverFunc(func(key routing.Key) (time.Duration, bool) {
		h, err := cfg.Routing.DecayHalfLife(key.Domain, key.Profile)
		return h, err == nil
	})
}
