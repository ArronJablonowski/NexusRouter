package config

import (
	"testing"
	"time"
)

func TestDaemonExecutionTimeoutConfiguration(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		want  time.Duration
		valid bool
	}{{"", 5 * time.Minute, true}, {"100ms", 100 * time.Millisecond, true}, {"30m", 30 * time.Minute, true}, {"0s", 0, false}, {"31m", 0, false}, {"invalid", 0, false}} {
		cfg := Defaults()
		cfg.Daemon.ExecutionTimeout = tc.raw
		got, err := cfg.Daemon.ExecutionDuration()
		if (err == nil) != tc.valid || got != tc.want {
			t.Fatalf("%q: %v %v", tc.raw, got, err)
		}
		if (cfg.Validate() == nil) != tc.valid {
			t.Fatalf("validation for %q", tc.raw)
		}
	}
}
