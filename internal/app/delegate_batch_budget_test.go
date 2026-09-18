package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/tools"
)

func TestDelegateBatchSharesAtomicParentBudget(t *testing.T) {
	for _, oversize := range []bool{false, true} {
		name := "single_then_batch"
		if oversize {
			name = "oversize_batch_no_partial_start"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			db, err := telemetry.Open(ctx, filepath.Join(t.TempDir(), "budget.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			registry := &tools.Registry{}
			cfg := config.Defaults()
			cfg.Workers.DelegateMaxCalls = 3
			var calls atomic.Int32
			if err = registerDelegate(registry, nil, db, db, cfg, "parent", "session", "", true, func(_ context.Context, prompt, validation, work string, local bool) (Result, error) {
				calls.Add(1)
				return Result{TaskID: "child-" + work, Text: prompt}, nil
			}, nil, nil); err != nil {
				t.Fatal(err)
			}
			executor := scopedDelegateTestExecutor{tools.Executor{Registry: registry, Policy: applicationToolPolicy()}}
			invoke := func(name, raw string) string {
				t.Helper()
				out, err := executor.Execute(ctx, providers.ToolCall{ID: "call", Name: name, Arguments: json.RawMessage(raw)})
				if err != nil {
					t.Fatal(err)
				}
				return out.Content
			}
			single := `{"prompt":"first","validation":"text"}`
			if out := invoke("delegate", single); !strings.Contains(out, `"untrusted_output":"first"`) || calls.Load() != 1 {
				t.Fatal(out, calls.Load())
			}
			if oversize {
				out := invoke("delegate_batch", `{"tasks":[{"prompt":"a","validation":"text"},{"prompt":"b","validation":"text"},{"prompt":"c","validation":"text"}]}`)
				if !strings.Contains(out, "delegate_unavailable_or_rejected") || calls.Load() != 1 {
					t.Fatal("partial oversize dispatch", out, calls.Load())
				}
			}
			out := invoke("delegate_batch", `{"tasks":[{"prompt":"second","validation":"text"},{"prompt":"third","validation":"text"}]}`)
			var batch struct {
				Results []struct {
					Output string `json:"untrusted_output"`
				} `json:"results"`
			}
			if json.Unmarshal([]byte(out), &batch) != nil || len(batch.Results) != 2 || batch.Results[0].Output != "second" || batch.Results[1].Output != "third" || calls.Load() != 3 {
				t.Fatal(out, calls.Load())
			}
			for _, tool := range []string{"delegate", "delegate_batch"} {
				raw := single
				if tool == "delegate_batch" {
					raw = `{"tasks":[{"prompt":"a","validation":"text"},{"prompt":"b","validation":"text"}]}`
				}
				if out := invoke(tool, raw); !strings.Contains(out, "delegate_unavailable_or_rejected") || calls.Load() != 3 {
					t.Fatal("budget escaped", out, calls.Load())
				}
			}
		})
	}
}
