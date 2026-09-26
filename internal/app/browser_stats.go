package app

import (
	"context"
	"github.com/ArronJablonowski/DarwinRouter/internal/usagestats"
)

func (s *Service) BrowserStats(ctx context.Context, reset *usagestats.Reset) (usagestats.Snapshot, error) {
	locality := make(map[[2]string]string)
	for _, m := range s.settings.Models {
		locality[[2]string{m.Provider, m.Model}] = m.Locality
	}
	return usagestats.Read(ctx, s.settings.Telemetry.Database, locality, reset)
}
