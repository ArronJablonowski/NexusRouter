package app

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/internal/usagestats"
)

func (s *Service) BrowserStats(ctx context.Context, reset *usagestats.Reset) (usagestats.Snapshot, error) {
	locality := make(map[[2]string]string)
	for _, m := range s.settings.Models {
		locality[[2]string{m.Provider, m.Model}] = m.Locality
	}
	out, err := usagestats.Read(ctx, s.settings.Telemetry.Database, locality, reset)
	if err == nil && s.settings.WebUI.RemoteTrustFile != "" {
		meter, e := usagestats.ReadRemoteUsage(ctx, s.settings.WebUI.RemoteTrustFile+".usage.db")
		if e != nil {
			out.RemoteUnavailable = true
		} else {
			out.Remote = &meter
		}
	}
	return out, err
}

func (s *Service) RemoteTaskUsage(ctx context.Context, tasks []string) (usagestats.RemoteUsage, error) {
	locality := make(map[[2]string]string)
	for _, m := range s.settings.Models {
		locality[[2]string{m.Provider, m.Model}] = m.Locality
	}
	return usagestats.TaskRemoteUsage(ctx, s.settings.Telemetry.Database, locality, tasks)
}
