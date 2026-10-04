package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

func TestScopedLocalInventoryRestrictsTrafficAndModels(t *testing.T) {
	var allowedCalls, deniedCalls atomic.Int32
	allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowedCalls.Add(1)
		if r.URL.Path != "/api/tags" {
			t.Error("unexpected discovery path", r.URL.Path)
		}
		fmt.Fprintf(w, `{"models":[{"name":"allowed:latest","size":1,"digest":%q},{"name":"denied:latest","size":1,"digest":%q}]}`, strings.Repeat("a", 64), strings.Repeat("b", 64))
	}))
	defer allowed.Close()
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { deniedCalls.Add(1); http.Error(w, "denied", 500) }))
	defer denied.Close()
	service := submissionService(t)
	service.settings.Providers = []config.Provider{{ID: "allowed", Kind: "ollama", Endpoint: allowed.URL}, {ID: "denied", Kind: "ollama", Endpoint: denied.URL}}
	scope := map[string]map[string]bool{"allowed": {"allowed:latest": true}, "denied": {"denied:latest": false}}
	got := service.scopedLocalModelInventory(context.Background(), scope)
	if len(got) != 1 || got["allowed"].err != nil || len(got["allowed"].models) != 1 || got["allowed"].models[0].Name != "allowed:latest" || allowedCalls.Load() != 1 || deniedCalls.Load() != 0 {
		t.Fatal("scoped inventory leaked or failed", len(got), allowedCalls.Load(), deniedCalls.Load())
	}
	got = service.scopedLocalModelInventory(context.Background(), map[string]map[string]bool{})
	if len(got) != 0 || allowedCalls.Load() != 1 || deniedCalls.Load() != 0 {
		t.Fatal("empty scope made traffic")
	}
	got = service.localModelInventory(context.Background())
	if len(got) != 2 || len(got["allowed"].models) != 2 || allowedCalls.Load() != 2 || deniedCalls.Load() == 0 {
		t.Fatal("local discovery behavior changed")
	}
}
