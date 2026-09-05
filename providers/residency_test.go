package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func residencyItem(name string) string {
	return fmt.Sprintf(`{"name":%q,"model":%q,"size":1024,"size_vram":512,"digest":%q,"expires_at":"2026-09-06T12:00:00Z","context_length":4096,"details":{"format":"gguf","family":"llama","families":["llama"],"parent_model":"","parameter_size":"7B","quantization_level":"Q4_0"}}`, name, name, strings.Repeat("a", 64))
}
func unloadAck() string {
	return `{"model":"llama:latest","created_at":"2026-09-06T12:00:00Z","response":"","done":true,"done_reason":"unload"}`
}

func TestResidentModelsAndConfirmedUnload(t *testing.T) {
	paths := []string{}
	p := fixtureProvider(t, "ollama", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer fixture-secret" {
			t.Error("credential header missing")
		}
		if r.URL.Path == "/api/generate" {
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil || !reflect.DeepEqual(body, map[string]any{"model": "llama", "prompt": "", "stream": false, "keep_alive": float64(0)}) {
				t.Error("unload invoked inference or altered target")
			}
			fmt.Fprint(w, unloadAck())
			return
		}
		if len(paths) == 1 {
			fmt.Fprint(w, `{"models":[`+residencyItem("llama:latest")+`]}`)
		} else {
			fmt.Fprint(w, `{"models":[]}`)
		}
	})
	models, err := p.ResidentModels(context.Background())
	if err != nil || len(models) != 1 || models[0].Size != 1024 || models[0].SizeVRAM != 512 {
		t.Fatal("residency metadata", err)
	}
	if err := p.UnloadModel(context.Background(), "llama"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{"GET /api/ps", "POST /api/generate", "GET /api/ps"}) {
		t.Fatal("unexpected operations", paths)
	}
}

func TestResidencyRejectsMalformedInventory(t *testing.T) {
	valid := `{"models":[` + residencyItem("llama") + `]}`
	for name, body := range map[string]string{
		"missing": `{}`, "null": `{"models":null}`, "duplicate": `{"models":[],"models":[]}`, "alias": `{"Models":[]}`, "trailing": `{"models":[]} {}`, "unknown": `{"models":[],"error":"private"}`,
		"duplicate model": `{"models":[` + residencyItem("llama") + `,` + residencyItem("llama:latest") + `]}`,
		"negative":        strings.Replace(valid, `"size":1024`, `"size":-1`, 1), "wrong identity": strings.Replace(valid, `"model":"llama"`, `"model":"other"`, 1), "duplicate nested": strings.Replace(valid, `"format":"gguf"`, `"format":"gguf","format":"bad"`, 1),
		"oversize": strings.Repeat("x", residencyLimit+1), "surrogate": strings.Replace(valid, `"parent_model":""`, `"parent_model":"\ud800"`, 1), "null model": `{"models":[null]}`,
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			p := fixtureProvider(t, "ollama", func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, body) })
			out, err := p.ResidentModels(context.Background())
			if err != ErrResidency || out != nil || calls != 1 {
				t.Fatal("malformed response accepted or retried", err, calls)
			}
		})
	}
}

func TestUnloadRequiresExplicitAcknowledgementAndAbsence(t *testing.T) {
	for _, mode := range []string{"still resident", "bad inventory", "wrong reason", "wrong model", "not done", "response", "duplicate", "null", "trailing", "status"} {
		t.Run(mode, func(t *testing.T) {
			posts, gets := 0, 0
			p := fixtureProvider(t, "ollama", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/ps" {
					gets++
					if mode == "bad inventory" {
						fmt.Fprint(w, `{}`)
					} else {
						fmt.Fprint(w, `{"models":[`+residencyItem("llama")+`]}`)
					}
					return
				}
				posts++
				body := unloadAck()
				switch mode {
				case "wrong reason":
					body = strings.Replace(body, "unload", "stop", 1)
				case "wrong model":
					body = strings.Replace(body, "llama:latest", "other", 1)
				case "not done":
					body = strings.Replace(body, `"done":true`, `"done":false`, 1)
				case "response":
					body = strings.Replace(body, `"response":""`, `"response":"generated text"`, 1)
				case "duplicate":
					body = strings.Replace(body, `"done":true`, `"done":true,"done":true`, 1)
				case "null":
					body = strings.Replace(body, `"response":""`, `"response":null`, 1)
				case "trailing":
					body += " {}"
				case "status":
					w.WriteHeader(500)
					body = "private provider error"
				}
				fmt.Fprint(w, body)
			})
			if err := p.UnloadModel(context.Background(), "llama"); err != ErrResidency || posts != 1 {
				t.Fatal("unconfirmed unload accepted or retried", err, posts)
			}
			wantGets := 0
			if mode == "still resident" || mode == "bad inventory" {
				wantGets = 1
			}
			if gets != wantGets {
				t.Fatal("unexpected confirmation read", gets)
			}
		})
	}
}

func TestResidencyUnsupportedCancellationAndRedirect(t *testing.T) {
	p := fixtureProvider(t, "openai_compatible", func(http.ResponseWriter, *http.Request) { t.Error("unsupported kind requested") })
	if _, err := p.ResidentModels(context.Background()); err != ErrResidency {
		t.Fatal("unsupported inspection admitted")
	}
	if err := p.UnloadModel(context.Background(), "llama"); err != ErrResidency {
		t.Fatal("unsupported unload admitted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p = fixtureProvider(t, "ollama", func(http.ResponseWriter, *http.Request) { t.Error("canceled request sent") })
	if _, err := p.ResidentModels(ctx); err != ErrResidency {
		t.Fatal("canceled inspection admitted")
	}
	if err := p.UnloadModel(ctx, "llama"); err != ErrResidency {
		t.Fatal("canceled unload admitted")
	}
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer target.Close()
	p = fixtureProvider(t, "ollama", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) })
	if err := p.UnloadModel(context.Background(), "llama"); err != ErrResidency || redirected.Load() != 0 {
		t.Fatal("redirect followed")
	}
	started := make(chan struct{})
	p = fixtureProvider(t, "ollama", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel = context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.UnloadModel(ctx, "llama") }()
	<-started
	cancel()
	if err := <-done; err != ErrResidency {
		t.Fatal("inflight cancel not surfaced safely")
	}
}

func TestOllamaModelIdentityConservative(t *testing.T) {
	for input, want := range map[string]string{"llama": "llama:latest", "namespace/llama": "namespace/llama:latest", "host:5000/ns/llama:7b": "host:5000/ns/llama:7b", "Llama:Tag": "Llama:Tag"} {
		got, err := OllamaModelIdentity(input)
		if err != nil || got != want {
			t.Fatal("identity changed", input, got, err)
		}
	}
	for _, bad := range []string{"", " llama", "llama ", "llama\n", "llama:latest:other", "a//b", "../llama", "llama:", ":tag", "llama@digest", "llama?x", string([]byte{255})} {
		if _, err := OllamaModelIdentity(bad); err == nil {
			t.Fatal("unsafe identity accepted", bad)
		}
	}
}

func TestResidentModelsAllowsIndefiniteFutureExpiry(t *testing.T) {
	body := `{"models":[` + strings.Replace(residencyItem("llama"), "2026-09-06", "2318-09-06", 1) + `]}`
	p := fixtureProvider(t, "ollama", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) })
	models, err := p.ResidentModels(context.Background())
	if err != nil || len(models) != 1 || models[0].ExpiresAt.Year() != 2318 {
		t.Fatal("far-future residency expiry refused", err)
	}
}
