package config

import (
	"testing"
	"time"
)

func TestProviderHealthHistoryDefaultsAndValidation(t *testing.T) {
	settings := Defaults()
	history := settings.Telemetry.ProviderHealthHistory
	interval, err := history.IntervalDuration()
	if err != nil || !history.Enabled || interval != 30*time.Second || history.Retain != 2880 {
		t.Fatal("unexpected provider health history defaults", history, interval, err)
	}
	if err = settings.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []ProviderHealthHistory{
		{Enabled: true, Interval: "4s", Retain: 10},
		{Enabled: true, Interval: "1h1s", Retain: 10},
		{Enabled: true, Interval: "bad", Retain: 10},
		{Enabled: true, Interval: "30s", Retain: 0},
		{Enabled: true, Interval: "30s", Retain: 100001},
	} {
		settings := Defaults()
		settings.Telemetry.ProviderHealthHistory = invalid
		if settings.Validate() == nil {
			t.Fatal("invalid provider health history accepted", invalid)
		}
	}
}
