package gridroute

import (
	"context"
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"maps"
	"time"
)

type catalogSnapshot struct {
	discovered remote.CandidateDiscovery
	infos      map[string]remote.Info
	expires    time.Time
}

// Cache only admission metadata, never imported quality. Dispatch performs fresh
// checks after its durable start. Trust changes invalidate the preview cache.
func (b *Bridge) catalog(ctx context.Context, r harness.Request) (remote.CandidateDiscovery, map[string]remote.Info, error) {
	registry, err := b.Client.Trust.Read()
	if err != nil {
		return remote.CandidateDiscovery{}, nil, err
	}
	// Peer authority is independent of rubric domain/profile. Evidence is loaded
	// separately for every exact task below. No exploration draw is cached.
	r.Task = harness.TaskClass{Domain: "general", Profile: "default", Difficulty: "unknown"}
	body, err := json.Marshal(struct {
		Request harness.Request
		Trust   string
	}{r, registry.Digest()})
	if err != nil {
		return remote.CandidateDiscovery{}, nil, err
	}
	key := string(body)
	b.mu.Lock()
	cached, ok := b.cache[key]
	b.mu.Unlock()
	if ok && time.Now().Before(cached.expires) {
		return cached.discovered, maps.Clone(cached.infos), nil
	}
	discovered, err := b.Client.DiscoverModelCandidates(ctx, r)
	if err != nil {
		return remote.CandidateDiscovery{}, nil, err
	}
	infos := map[string]remote.Info{}
	for _, c := range discovered.Candidates {
		if _, ok := infos[c.Destination]; ok {
			continue
		}
		info, e := b.Client.Info(ctx, c.Destination)
		if e == nil {
			infos[c.Destination] = info
		}
	}
	if ctx.Err() != nil {
		return remote.CandidateDiscovery{}, nil, ctx.Err()
	}
	b.mu.Lock()
	if b.cache == nil || len(b.cache) > 32 {
		b.cache = map[string]catalogSnapshot{}
	}
	b.cache[key] = catalogSnapshot{discovered, infos, time.Now().Add(10 * time.Second)}
	b.mu.Unlock()
	return discovered, maps.Clone(infos), nil
}
