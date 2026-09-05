package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/providers"
)

func TestChatUsageProviderClientRoundTrip(t *testing.T) {
	s := services()
	s.RunTextStream = func(_ context.Context, _ app.Request, emit func(string) error) (app.Result, error) {
		if err := emit("hello"); err != nil {
			return app.Result{}, err
		}
		return app.Result{Text: "hello", FinishReason: "stop", Usage: &providers.Usage{InputTokens: 7, OutputTokens: 3}}, nil
	}
	h, _ := New(token, 1, s)
	server := httptest.NewServer(h)
	defer server.Close()
	client, err := providers.NewHTTP(server.URL+"/v1", "openai_compatible", token, server.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var text string
	var usage *providers.Usage
	done := false
	err = client.Stream(ctx, providers.Request{Model: "m", Messages: []providers.Message{{Role: "user", Content: "hello"}}}, func(c providers.Chunk) error {
		text += c.Text
		if c.Usage != nil {
			if done || usage != nil {
				t.Error("usage delivered twice or after done")
			}
			usage = c.Usage
		}
		if c.Done {
			if usage == nil || c.FinishReason != "stop" {
				t.Error("completion preceded usage")
			}
			done = true
		}
		return nil
	})
	if err != nil || text != "hello" || !done || usage == nil || *usage != (providers.Usage{InputTokens: 7, OutputTokens: 3}) {
		t.Fatal(text, done, usage, err)
	}
}

type usageBoundaryWriter struct {
	recorder *httptest.ResponseRecorder
	writes   int
	mode     string
}

func (w *usageBoundaryWriter) Header() http.Header  { return w.recorder.Header() }
func (w *usageBoundaryWriter) WriteHeader(code int) { w.recorder.WriteHeader(code) }
func (w *usageBoundaryWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == 4 {
		switch w.mode {
		case "panic":
			panic("private failure")
		case "short":
			return 0, nil
		case "write":
			return 0, errors.New("private failure")
		}
	}
	return w.recorder.Write(p)
}
func (w *usageBoundaryWriter) FlushError() error {
	if w.writes == 4 && w.mode == "flush" {
		return errors.New("private failure")
	}
	w.recorder.Flush()
	return nil
}

func TestChatUsageFinalFrameDeliveryFailureHasNoDone(t *testing.T) {
	for _, mode := range []string{"panic", "short", "write", "flush"} {
		t.Run(mode, func(t *testing.T) {
			s := services()
			s.RunTextStream = func(_ context.Context, _ app.Request, emit func(string) error) (app.Result, error) {
				if err := emit("answer"); err != nil {
					return app.Result{}, err
				}
				return app.Result{Text: "answer", FinishReason: "stop", Usage: &providers.Usage{}}, nil
			}
			h, _ := New(token, 1, s)
			w := &usageBoundaryWriter{recorder: httptest.NewRecorder(), mode: mode}
			h.ServeHTTP(w, request("POST", "/v1/chat/completions", chatUsageRequest(`{"include_usage":true}`)))
			if w.writes != 4 || strings.Contains(w.recorder.Body.String(), "[DONE]") || strings.Contains(w.recorder.Body.String(), "private") || len(h.slots) != 0 {
				t.Fatal(w.writes, w.recorder.Body.String())
			}
		})
	}
}
