package api

import (
	"context"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/resources"
)

type apiFixtureProfiler struct{}

func (apiFixtureProfiler) Measure(context.Context) (resources.Measurement, error) {
	return resources.Measurement{Version: 1, Snapshot: resources.Snapshot{
		Time: time.Now().UTC(), CPUs: 2, TotalRAM: 8 << 30, AvailableRAM: 8 << 30, Source: "api-fixture",
	}}, nil
}

// newAPIFixtureService keeps functional HTTP integration tests independent of
// live host pressure. Dedicated resource tests construct their own services.
func newAPIFixtureService(settings config.Settings, secret func(string) string) (*app.Service, error) {
	settings.Hardware.AutoProfile = false
	return app.NewServiceWithProfiler(settings, secret, apiFixtureProfiler{})
}
