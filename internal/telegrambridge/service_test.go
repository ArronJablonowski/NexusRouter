package telegrambridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/connectors/telegram"
	"github.com/ArronJablonowski/NexusRouter/health"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

type testProfiler struct{}

func (testProfiler) Measure(context.Context) (resources.Measurement, error) {
	return resources.Measurement{Version: 1, Snapshot: resources.Snapshot{Time: time.Now(), CPUs: 8, TotalRAM: 64 << 30, AvailableRAM: 48 << 30, SwapUsed: new(uint64), RAMReserveBytes: 4 << 30}}, nil
}

func TestRealDurableSubmissionsContinueOnlyTelegramHistory(t *testing.T) {
	var mu sync.Mutex
	var prompts [][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			fmt.Fprint(w, `{"models":[{"name":"fixture"}]}`)
			return
		}
		var body struct {
			Messages []struct{ Role, Content string }
			Tools    []any
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid request")
		}
		if len(body.Tools) != 0 {
			t.Error("Telegram model received host tools")
		}
		var values []string
		for _, m := range body.Messages {
			values = append(values, m.Content)
		}
		mu.Lock()
		prompts = append(prompts, values)
		mu.Unlock()
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":"fixture answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	s := config.Defaults()
	s.Telemetry.Database = filepath.Join(t.TempDir(), "commander.db")
	s.Telegram = config.Telegram{StateDirectory: filepath.Join(t.TempDir(), "telegram"), TokenEnv: "TELEGRAM_TOKEN", ModelID: "fixture"}
	s.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: server.URL}}
	zero := 0.0
	s.Models = []config.Model{{ID: "fixture", Model: "fixture", Provider: "local", Locality: "local", Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero, RAMBytes: 64 << 20}}
	settings := chatSettings(s)
	strict := false
	settings.Hardware.BackendManagedMemory = &strict
	service, err := app.NewServiceWithProfiler(settings, nil, testProfiler{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := openStore(s.Telegram.StateDirectory, identity("test-bot", "fixture"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { st.close() }()
	dispatcher, err := app.StartDispatcher(ctx, service)
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	api := &fakeAPI{updates: []telegram.Update{update(1, 7, "remember synthetic marker ORBIT"), update(2, 7, "recall the previous message")}}
	b := &bridge{store: st, api: api, backend: serviceBackend{service: service, model: "fixture", policyDB: s.Telemetry.Database}, allowed: map[int64]bool{7: true}}
	// Lose the first durable admission acknowledgement, then restart only the
	// connector observer while the dispatcher retains ownership of that task.
	b.backend = &lostAcknowledgementBackend{inner: b.backend}
	if b.step(ctx) == nil {
		t.Fatal("missing simulated acknowledgement loss")
	}
	st.close()
	st, err = openStore(s.Telegram.StateDirectory, identity("test-bot", "fixture"))
	if err != nil {
		t.Fatal(err)
	}
	b.store = st
	for b.store.value.Offset < 3 && ctx.Err() == nil {
		step(t, b)
		select {
		case <-ctx.Done():
		case <-time.After(25 * time.Millisecond):
		}
	}
	if ctx.Err() != nil {
		t.Fatal("durable dispatch failed to finish", ctx.Err())
	}
	if len(api.sent) != 2 || api.sent[0] != "fixture answer" || api.sent[1] != "fixture answer" {
		t.Fatal("missing final answers", api.sent)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 2 {
		t.Fatal("duplicate model calls", len(prompts))
	}
	joined := strings.Join(prompts[1], "\n")
	if !strings.Contains(joined, "ORBIT") || !strings.Contains(joined, "recall the previous message") || !strings.Contains(joined, "fixture answer") {
		t.Fatal("history lost", joined)
	}
}

func TestEnabledMissingTokenFailsBeforeCreatingState(t *testing.T) {
	s := config.Defaults()
	zero := 0.0
	s.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: "http://127.0.0.1:1"}}
	s.Models = []config.Model{{ID: "fixture", Model: "fixture", Provider: "local", Locality: "local", Capabilities: []string{"chat"}, ContextTokens: 8192, RAMBytes: 1 << 20, EstimatedCost: &zero}}
	s.Telegram = config.Telegram{Enabled: true, ModelID: "fixture", TokenEnv: "TELEGRAM_TOKEN", AllowedUsers: []int64{7}, StateDirectory: filepath.Join(t.TempDir(), "absent")}
	w, err := Start(context.Background(), s, func(string) string { return "" }, "test", func(string) { t.Fatal("connected") })
	if err != ErrConfiguration || w != nil {
		t.Fatal(w, err)
	}
}

func TestWorkerHealthDoesNotExposeIdentity(t *testing.T) {
	var disabled *Worker
	if c := disabled.Health(); c.Validate() != nil || c.Status != "disabled" {
		t.Fatal(c)
	}
	w := &Worker{}
	for _, tc := range []struct {
		state  int32
		status string
	}{{0, "unknown"}, {1, "healthy"}, {2, "degraded"}, {3, "unavailable"}} {
		w.state.Store(tc.state)
		c := w.Health()
		if c.Validate() != nil || c.Component != "telegram" || c.ID != "" || c.Status != tc.status {
			t.Fatal(c)
		}
	}
}

type lostAcknowledgementBackend struct {
	inner backend
	lost  bool
}

func (b *lostAcknowledgementBackend) Submit(ctx context.Context, key, text, previous string) (submissions.Status, error) {
	status, err := b.inner.Submit(ctx, key, text, previous)
	if err == nil && !b.lost {
		b.lost = true
		return submissions.Status{}, errors.New("simulated lost acknowledgement")
	}
	return status, err
}
func (b *lostAcknowledgementBackend) Status(ctx context.Context, id string) (submissions.Status, error) {
	return b.inner.Status(ctx, id)
}

func TestTelegramOutageDegradesHealthWithoutBlockingOtherChats(t *testing.T) {
	checks := []health.Check{
		{Component: "daemon", Status: "healthy", Code: "serving"},
		{Component: "database", Status: "healthy", Code: "available"},
		{Component: "supervisor", Status: "healthy", Code: "supervisor_ok"},
		{Component: "resources", Status: "healthy", Code: "capacity_available"},
		{Component: "model", ID: "fixture", Status: "healthy", Code: "available"},
	}
	worker := &Worker{}
	worker.state.Store(2)
	checks = append(checks, worker.Health())
	if status, ready := health.Outcome(checks); status != "degraded" || !ready {
		t.Fatal(status, ready)
	}
}
