package pi

import (
	"context"
	"strings"
	"testing"
	"time"
)

func identityConfig() Config {
	return Config{Prices: &Prices{Input: 1, Output: 2}, Executable: "/test/pi", ExecutableSHA256: strings.Repeat("a", 64), Provider: "fixture", Model: "model", ModelRevision: "weights-v1", BaseURL: "http://127.0.0.1/v1", APIKey: "secret-one", ContextTokens: 16384, MaxOutputTokens: 1024, Timeout: time.Minute, Admit: func(context.Context) (func(), error) { return func() {}, nil }}
}
func TestEffectiveIdentityIsolation(t *testing.T) {
	cfg := identityConfig()
	first, err := cfg.Identity()
	if err != nil {
		t.Fatal(err)
	}
	cfg.APIKey = "secret-two"
	rotated, err := cfg.Identity()
	if err != nil || first != rotated {
		t.Fatal("credential rotation changed learning identity", err)
	}
	changes := map[string]func(*Config){
		"model revision": func(c *Config) { c.ModelRevision = "weights-v2" },
		"artifact":       func(c *Config) { c.ExecutableSHA256 = strings.Repeat("b", 64) },
		"endpoint":       func(c *Config) { c.BaseURL = "http://127.0.0.2/v1" },
		"context":        func(c *Config) { c.ContextTokens = 32768 },
		"output":         func(c *Config) { c.MaxOutputTokens = 2048 },
		"deadline":       func(c *Config) { c.Timeout = 2 * time.Minute },
		"prices":         func(c *Config) { c.Prices.Output = 3 },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			c := identityConfig()
			change(&c)
			id, e := c.Identity()
			if e != nil || id == first {
				t.Fatal("changed deployment borrowed prior identity", e)
			}
		})
	}
	cfg.ModelRevision = ""
	if _, err := cfg.Identity(); err == nil {
		t.Fatal("missing deployment revision accepted")
	}
}
func TestUsageRejectsMissingOrInconsistentCounts(t *testing.T) {
	n := func(v int64) *int64 { return &v }
	valid := func() *Usage {
		return &Usage{Input: n(20), Output: n(4), CacheRead: n(2), CacheWrite: n(1), TotalTokens: n(27), Reasoning: n(2), CacheWrite1h: n(1), Cost: &UsageCost{}}
	}
	if !valid().valid() {
		t.Fatal("valid counts rejected")
	}
	for name, change := range map[string]func(*Usage){
		"missing":                func(u *Usage) { u.Input = nil },
		"negative":               func(u *Usage) { u.Output = n(-1) },
		"total":                  func(u *Usage) { u.TotalTokens = n(24) },
		"reasoning double count": func(u *Usage) { u.Reasoning = n(5) },
		"cache subset":           func(u *Usage) { u.CacheWrite1h = n(2) },
		"missing cost":           func(u *Usage) { u.Cost = nil },
	} {
		t.Run(name, func(t *testing.T) {
			u := valid()
			change(u)
			if u.valid() {
				t.Fatal("invalid usage accepted")
			}
		})
	}
}
