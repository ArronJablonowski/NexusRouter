package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/resources"
)

func TestWarmMemoryUsesQualifiedBudgetAndPreservesAdmission(t *testing.T) {
	digest := strings.Repeat("a", 64)
	for _, mode := range []string{"warm", "absent", "digest", "context", "expiry", "inventory", "other-resident", "discrete", "pressure", "overhead", "unavailable", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("unexpected mutation")
					w.WriteHeader(405)
					return
				}
				if mode == "unavailable" {
					w.WriteHeader(503)
					return
				}
				if r.URL.Path == "/api/tags" {
					d := digest
					if mode == "inventory" {
						d = strings.Repeat("b", 64)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]any{"name": "large:latest", "size": 1, "digest": d, "modified_at": time.Now().UTC()}}})
					return
				}
				if r.URL.Path != "/api/ps" {
					t.Error("unexpected endpoint", r.URL.Path)
					w.WriteHeader(404)
					return
				}
				resident := map[string]any{"name": "large:latest", "model": "large:latest", "size": uint64(1 << 30), "size_vram": uint64(1 << 30), "digest": digest, "context_length": 8192, "expires_at": time.Now().Add(48 * time.Hour)}
				if mode == "oversize" {
					resident["size"] = uint64(100 << 30)
				}
				if mode == "digest" {
					resident["digest"] = strings.Repeat("b", 64)
				}
				if mode == "context" {
					resident["context_length"] = 4096
				}
				if mode == "expiry" {
					resident["expires_at"] = time.Now().Add(5 * time.Minute)
				}
				models := []any{resident}
				if mode == "absent" {
					models = []any{}
				}
				if mode == "other-resident" {
					models = append(models, resident)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
			}))
			defer server.Close()
			service, _ := autoFixture(t)
			service.settings.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: server.URL, DedicatedWarmMemory: true}}
			model := config.Model{ID: "large", Provider: "local", Model: "large:latest", Locality: "local", RAMBytes: 64 << 30, WarmRAMBytes: 32 << 30, ResidencyDigest: digest, ContextTokens: 8192, Capabilities: []string{"chat"}}
			service.settings.Models = []config.Model{model}
			service.settings.Hardware.Concurrent = "1"
			service.settings.Workers.Max = 1
			service.providerFactory = nil
			limits := resources.Limits{MaxConcurrent: 1, RAMPercent: 90, VRAMPercent: 90, MaxAge: time.Second}
			service.budget, _ = resources.NewBudget(limits)
			service.profile = func(context.Context) (resources.Snapshot, error) {
				pressure := mode == "pressure"
				return resources.Snapshot{Time: time.Now(), CPUs: 4, TotalRAM: 128 << 30, AvailableRAM: 60 << 30, UnifiedMemory: mode != "discrete", ThermalPressure: &pressure}, nil
			}
			fixture := newReservationCoordinatorFixture()
			installReservationFixture(t, service, fixture)
			if mode == "overhead" {
				model.RAMBytes += 1 << 30
			}
			ctx := context.Background()
			_, release, err := service.reservePrimary(ctx, ctx, model, Request{taskID: "task-warm", sessionID: "session-warm", Profile: "default", ContextTokens: 8192})
			if mode != "warm" {
				if err == nil || release != nil || len(fixture.requests) != 0 {
					t.Fatal("unsafe warm admission", err, fixture.requests)
				}
				return
			}
			if err != nil || release == nil || len(fixture.requests) != 1 || fixture.requests[0].RAMBytes != 32<<30 {
				t.Fatal("qualified budget not reserved", err, fixture.requests)
			}
			_, second, err := service.reservePrimary(ctx, ctx, model, Request{taskID: "task-second", sessionID: "session-second", Profile: "default", ContextTokens: 8192})
			if !errors.Is(err, resources.ErrCapacity) || second != nil {
				t.Fatal("warm path bypassed concurrency", err)
			}
			if err := release(); err != nil {
				t.Fatal(err)
			}
			if err := release(); err != nil || fixture.releases != 1 {
				t.Fatal("release was not idempotent", err)
			}
		})
	}
}
