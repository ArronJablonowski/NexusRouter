package v1_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
)

func TestSDKNativePiOllamaContextAndTerminal(t *testing.T) {
	if os.Getenv("NEXUS_PI_NATIVE") != "1" {
		t.Skip("requires installed Pi qualification")
	}
	for _, mode := range []string{"valid", "truncated", "wrong_model", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var request struct {
					Model    string
					Think    *bool
					Stream   bool
					Messages []struct{ Role, Content string }
					Options  struct {
						Context int `json:"num_ctx"`
						Output  int `json:"num_predict"`
					}
					KeepAlive json.RawMessage `json:"keep_alive"`
				}
				if json.NewDecoder(r.Body).Decode(&request) != nil || r.URL.Path != "/api/chat" || request.Model != "fixture" || request.Think == nil || *request.Think || !request.Stream || request.Options.Context != 16384 || request.Options.Output != 1024 || len(request.Messages) != 1 || request.Messages[0].Content != "Write an answer." || request.Messages[0].Role != "user" || len(request.KeepAlive) != 0 || r.Header.Get("Authorization") != "Bearer native-fixture-secret" {
					t.Error("native context/output/credential contract changed")
					http.Error(w, "bad", 400)
					return
				}
				w.Header().Set("Content-Type", "application/x-ndjson")
				if mode == "cancel" {
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
					cancel()
					<-r.Context().Done()
					return
				}
				model := "fixture"
				if mode == "wrong_model" {
					model = "other"
				}
				fmt.Fprintf(w, "{\"model\":%q,\"message\":{\"role\":\"assistant\",\"content\":\"native Ollama answer\"},\"done\":false}\n", model)
				if mode != "truncated" {
					fmt.Fprintf(w, "{\"model\":%q,\"message\":{\"role\":\"assistant\",\"content\":\"\"},\"done\":true,\"done_reason\":\"stop\",\"prompt_eval_count\":10,\"eval_count\":4}\n", model)
				}
			}))
			defer server.Close()
			client, _ := nativeSDKProviderClient(t, server.URL, 64<<20, false, "ollama")
			result, err := client.Run(ctx, sdk.Request{Version: 1, HarnessID: "pi-fixture", ModelID: "chat", Prompt: "Write an answer.", Domain: "writing"})
			if calls.Load() != 1 {
				t.Fatal("unexpected inference count", calls.Load(), err)
			}
			if mode == "valid" {
				if err != nil || result.Text != "native Ollama answer" || result.HarnessOutcome == nil || result.Usage == nil || result.Usage.InputTokens != 10 || result.Usage.OutputTokens != 4 {
					t.Fatal(result, err)
				}
			} else if err == nil || result.Text != "" || result.HarnessOutcome != nil || strings.Contains(err.Error(), "private") {
				t.Fatal("unverified native output accepted", result, err)
			}
			page, e := client.ReadEvents(context.Background(), result.TaskID, 0, 10)
			if e != nil || len(page.Events) != 2 || (page.State == "completed") != (mode == "valid") {
				t.Fatal(page, e)
			}
		})
	}
}
