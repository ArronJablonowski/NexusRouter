package gridcollab

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/internal/agentchat"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"slices"
	"sync"
	"time"
)

type messageClient interface {
	Collaboration(context.Context, string, string, int64) (agentchat.Batch, error)
}
type Syncer struct {
	mu        sync.Mutex
	next      int
	Trust     trustReader
	Client    messageClient
	Directory string
}

func (s *Syncer) Sync(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	registry, e := s.Trust.Read()
	if e != nil {
		return e
	}
	store, e := agentchat.Open(ctx, s.Directory)
	if e != nil {
		return e
	}
	defer store.Close()
	for i := 0; i < len(registry.Peers); i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		index := s.next % len(registry.Peers)
		p := registry.Peers[index]
		s.next = (index + 1) % len(registry.Peers)
		// Never expand paired permissions. Skipped hosts appear as unavailable.
		if !slices.Contains(p.Operations, "logs") {
			if e = store.SyncFailure(ctx, p.ID); e != nil {
				return e
			}
			continue
		}
		stream, after, e := store.Cursor(ctx, p.ID)
		if e != nil {
			return e
		}
		for page := 0; page < 4; page++ {
			call, cancel := context.WithTimeout(ctx, 3*time.Second)
			batch, err := s.Client.Collaboration(call, p.ID, stream, after)
			cancel()
			if err != nil {
				if e = store.SyncFailure(ctx, p.ID); e != nil {
					return e
				}
				break
			}
			if e = store.Import(ctx, p.ID, batch); e != nil {
				_ = store.SyncFailure(ctx, p.ID)
				break
			}
			stream, after = batch.Stream, batch.Next
			if len(batch.Messages) < 50 {
				break
			}
			if page == 3 {
				if e = store.SyncPending(ctx, p.ID); e != nil {
					return e
				}
			}
		}
	}
	return ctx.Err()
}

// StartSync follows daemon lifetime; no models or tasks are started by polling.
func StartSync(ctx context.Context, s *app.Service, cfg config.Settings) {
	c := cfg.WebUI.RemoteClient
	if !cfg.Tools.CollaborationEnabled || c == nil || cfg.WebUI.RemoteTrustFile == "" {
		return
	}
	syncer := &Syncer{Trust: remote.TrustFile(cfg.WebUI.RemoteTrustFile), Client: &remote.Client{Trust: remote.TrustFile(cfg.WebUI.RemoteTrustFile), UsageFile: cfg.WebUI.RemoteTrustFile + ".usage.db", Credentials: remote.Credentials{CertificateFile: c.CertificateFile, KeyFile: c.KeyFile, CAFile: c.CAFile}}, Directory: cfg.Telemetry.Database + ".collaboration"}
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			round, cancel := context.WithTimeout(ctx, 15*time.Second)
			_ = syncer.Sync(round)
			cancel()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
