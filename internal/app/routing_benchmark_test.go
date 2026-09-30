package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

// BenchmarkAutomaticTaskOverhead measures full automatic Service.Run tasks,
// including admission, SQLite reads/writes, loopback HTTP, and task execution.
// It excludes real inference, resource sensors, and live providers. The resource
// profile is mocked with stable capacity and a fresh timestamp on every call.
// Discovery is warmed by one completed service task before timing. Seed tasks
// use the real runtime and SQLite journal with an in-process provider, including
// the deterministic nonempty-text check. Every timed iteration adds a new task,
// so the corpus grows: finaltasks includes seeds, warmup, and timed iterations.
// Model pools of two and eight candidates expose pool-size routing overhead.
// Reproduce with:
//
//	go test ./internal/app -run '^$' -bench BenchmarkAutomaticTaskOverhead -benchtime=100x -count=1
func BenchmarkAutomaticTaskOverhead(b *testing.B) {
	for _, poolSize := range []int{2, 8} {
		b.Run(fmt.Sprintf("pool_%d", poolSize), func(b *testing.B) {
			for _, seedTasks := range []int{0, 100, 1000} {
				b.Run(fmt.Sprintf("seed_%d", seedTasks), func(b *testing.B) {
					ctx := context.Background()
					ids := make([]string, poolSize)
					tags := make([]map[string]string, poolSize)
					for i := range ids {
						ids[i] = string(rune('a' + i))
						tags[i] = map[string]string{"name": ids[i]}
					}
					tagsJSON, err := json.Marshal(map[string]any{"models": tags})
					if err != nil {
						b.Fatal(err)
					}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						switch r.URL.Path {
						case "/api/tags":
							_, _ = w.Write(tagsJSON)
						case "/api/chat":
							fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
						default:
							http.NotFound(w, r)
						}
					}))
					b.Cleanup(server.Close)
					cfg := config.Defaults()
					cfg.Mode = "local_only"
					cfg.Routing.Exploration = 0
					cfg.Telemetry.Database = filepath.Join(b.TempDir(), "routing-benchmark.db")
					cfg.Providers = []config.Provider{{ID: "local", Kind: "ollama", Endpoint: server.URL}}
					zero := 0.0
					for _, id := range ids {
						cfg.Models = append(cfg.Models, config.Model{ID: id, Model: id, Provider: "local", Locality: "local", Capabilities: []string{"chat"}, ContextTokens: 8192, EstimatedCost: &zero, RAMBytes: 100})
					}
					db, err := telemetry.Open(ctx, cfg.Telemetry.Database)
					if err != nil {
						b.Fatal(err)
					}
					loop := runtime.Loop{Provider: benchmarkImmediateProvider{}, Journal: db}
					for i := 0; i < seedTasks; i++ {
						id := fmt.Sprintf("seed-%d", i)
						model := cfg.Models[i%len(cfg.Models)].Model
						out, err := loop.Run(ctx, runtime.RunRequest{
							TaskID: id, SessionID: id, ProviderID: "local", Privacy: "local_only",
							Domain: "general", Profile: "default", RequireText: true,
							Inference: providers.Request{Model: model, Messages: []providers.Message{{Role: "user", Content: "hello"}}},
							MaxTurns:  1, MaxContextTokens: 8192, MaxOutputBytes: 1 << 20,
						})
						if err != nil || out.Text != "answer" {
							_ = db.Close()
							b.Fatalf("seed task: result=%+v error=%v", out, err)
						}
					}
					if err := db.Close(); err != nil {
						b.Fatal(err)
					}
					svc, err := NewService(cfg, nil)
					if err != nil {
						b.Fatal(err)
					}
					svc.profile = func(context.Context) (resources.Snapshot, error) {
						return resources.Snapshot{Time: time.Now(), TotalRAM: 1000, AvailableRAM: 1000}, nil
					}
					svc.draw = func() float64 { return 0 }
					req := Request{ModelID: "auto", Prompt: "hello"}
					if out, err := svc.Run(ctx, req); err != nil || out.Text != "answer" || out.TaskID == "" {
						b.Fatalf("warmup task: result=%+v error=%v", out, err)
					}
					latencies := make([]time.Duration, b.N)
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						started := time.Now()
						if out, err := svc.Run(ctx, req); err != nil || out.Text != "answer" || out.TaskID == "" {
							b.Fatalf("automatic task: result=%+v error=%v", out, err)
						}
						latencies[i] = time.Since(started)
					}
					b.StopTimer()
					reportTaskLatencies(b, latencies)
					b.ReportMetric(float64(seedTasks+1+b.N), "finaltasks")
				})
			}
		})
	}
}

type benchmarkImmediateProvider struct{}

func (benchmarkImmediateProvider) Models(context.Context) ([]string, error) {
	return nil, nil // Runtime seed execution does not perform discovery.
}

func (benchmarkImmediateProvider) Stream(_ context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
	return emit(providers.Chunk{Text: "answer", Done: true, FinishReason: "stop"})
}
