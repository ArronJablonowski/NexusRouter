package remotecli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/resources"
)

func TestFreshScopedInventory(t *testing.T) {
	var calls atomic.Int32
	var missing atomic.Bool
	var failed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/api/tags" {
			t.Error("non-discovery request", r.Method, r.URL.Path)
		}
		if failed.Load() {
			http.Error(w, "sensitive provider error", 500)
			return
		}
		if missing.Load() {
			fmt.Fprint(w, `{"models":[]}`)
		} else {
			fmt.Fprint(w, `{"models":[{"name":"fixture"}]}`)
		}
	}))
	defer server.Close()
	cfg := config.Settings{Mode: "local_only", Providers: []config.Provider{{ID: "local", Kind: "ollama", Endpoint: server.URL}}}
	observer := modelObserver(cfg, nil, func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now().UTC(), TotalRAM: 100, AvailableRAM: 40}, nil
	})
	models := []remote.Model{{ID: "one", Provider: "local", Model: "fixture", Local: true}, {ID: "two", Provider: "local", Model: "missing", Local: true}}
	result, ram, err := observer(context.Background(), models)
	if err != nil || result[0].State != "present" || result[1].State != "absent" || calls.Load() != 1 || ram.State != "measured" || *ram.AvailableRAM != 40 {
		t.Fatal(result, ram, err, calls.Load())
	}
	missing.Store(true)
	result, _, err = observer(context.Background(), models)
	if err != nil || result[0].State != "absent" || calls.Load() != 2 {
		t.Fatal("stale inventory", result, err)
	}
	failed.Store(true)
	result, _, err = observer(context.Background(), models)
	if err != nil || result[0].State != "unknown" {
		t.Fatal("failure became absence/presence", result, err)
	}
	before := calls.Load()
	_, _, err = observer(context.Background(), nil)
	if err != nil || calls.Load() != before {
		t.Fatal("unrequested provider probed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err = observer(ctx, models); err == nil || calls.Load() != before {
		t.Fatal("cancellation ignored")
	}
}
func TestUnknownResourcesAndMissingCredential(t *testing.T) {
	cfg := config.Settings{Mode: "local_only", Providers: []config.Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:1", APIKeyEnv: "SECRET"}}}
	observer := modelObserver(cfg, func(string) string { return "" }, func(context.Context) (resources.Snapshot, error) { return resources.Snapshot{}, resources.ErrProfile })
	obs, ram, err := observer(context.Background(), []remote.Model{{ID: "one", Provider: "local", Model: "fixture", Local: true}})
	if err != nil || obs[0].State != "unknown" || ram.State != "unknown" || ram.TotalRAM != nil || ram.AvailableRAM != nil {
		t.Fatal(obs, ram, err)
	}
}
