package config

import "testing"

func TestFallbackRecoveryConfigurationBounds(t *testing.T) {
	for _, c := range []struct {
		attempts int
		timeout  string
		valid    bool
	}{{0, "", true}, {1, "100ms", true}, {8, "30m", true}, {-1, "1m", false}, {9, "1m", false}, {3, "99ms", false}, {3, "31m", false}, {3, "invalid", false}} {
		cfg := Defaults()
		cfg.Runtime.FallbackMaxAttempts = c.attempts
		cfg.Runtime.FallbackTimeout = c.timeout
		if (cfg.Validate() == nil) != c.valid {
			t.Fatal(c, cfg.Validate())
		}
	}
}

func TestFallbackRecoveryConfigLoadPrecedence(t *testing.T) {
	cfg, err := Load(Options{ProjectFile: file(t, "runtime:\n  fallback_max_attempts: 2\n  fallback_timeout: 2m\n"), Env: map[string]string{"runtime.fallback_max_attempts": "4"}, Flags: map[string]string{"runtime.fallback_timeout": "30s"}})
	if err != nil || cfg.Runtime.FallbackMaxAttempts != 4 || cfg.Runtime.FallbackTimeout != "30s" {
		t.Fatal(cfg.Runtime, err)
	}
}
