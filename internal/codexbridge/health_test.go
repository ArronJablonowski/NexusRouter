package codexbridge

import (
	"context"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/internal/codexrpc"
	"os"
	"testing"
	"time"
)

func TestHealthModelsReadOnly(t *testing.T) {
	for _, tc := range []struct {
		name, account, catalog string
		wantErr                bool
	}{
		{"signed in", `{"account":{"type":"chatgpt"}}`, `{"data":[{"model":"gpt-5.6-sol"}],"nextCursor":null}`, false},
		{"signed out", `{"account":null}`, `{}`, true},
		{"malformed catalog", `{"account":{"type":"chatgpt"}}`, `{}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &scriptedSessionWire{closed: make(chan struct{}), frames: []codexrpc.Envelope{
				sessionResponse("1", `{"userAgent":"fixture"}`), sessionResponse("2", tc.account), sessionResponse("3", tc.catalog)}}
			names, err := healthModels(context.Background(), w, t.TempDir())
			if (err != nil) != tc.wantErr {
				t.Fatal(names, err)
			}
			if !tc.wantErr && (len(names) != 1 || names[0] != "gpt-5.6-sol") {
				t.Fatal(names)
			}
			for _, e := range w.sent() {
				switch e.Method {
				case "initialize", "initialized", "account/read", "model/list":
				default:
					t.Fatal("unexpected operation", e.Method)
				}
			}
			select {
			case <-w.closed:
			default:
				t.Fatal("wire leaked")
			}
			if tc.name == "signed out" && !errors.Is(err, ErrCredentialsMissing) {
				t.Fatal(err)
			}
		})
	}
}
func TestHealthModelsCancellation(t *testing.T) {
	w := &scriptedSessionWire{closed: make(chan struct{}), block: true}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := healthModels(ctx, w, t.TempDir()); err == nil {
		t.Fatal("expected cancellation")
	}
}
func TestLiveHealthModels(t *testing.T) {
	if os.Getenv("DARWIN_CODEX_HEALTH_LIVE") != "1" {
		t.Skip("opt-in discovery only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	names, err := HealthModels(ctx, "/Users/aj_lobster/.local/bin/codex")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name == "gpt-5.6-sol" {
			return
		}
	}
	t.Fatal("configured model missing")
}

func TestHealthAccountNotices(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		count      int
		fail       bool
	}{
		{"valid", `{"authMode":"chatgpt","planType":"pro"}`, 1, false},
		{"signed out notice", `{"authMode":null,"planType":null}`, 1, false},
		{"malformed", `{"authMode":17,"planType":"pro"}`, 1, true},
		{"unknown field", `{"authMode":"chatgpt","planType":"pro","extra":true}`, 1, true},
		{"flood", `{"authMode":"chatgpt","planType":"pro"}`, 17, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frames := []codexrpc.Envelope{sessionResponse("1", `{"userAgent":"fixture"}`)}
			for i := 0; i < tc.count; i++ {
				frames = append(frames, codexrpc.Envelope{Method: "account/updated", Params: []byte(tc.body)})
			}
			frames = append(frames, sessionResponse("2", `{"account":{"type":"chatgpt"}}`), sessionResponse("3", `{"data":[{"model":"gpt-5.6-sol"}],"nextCursor":null}`))
			w := &scriptedSessionWire{closed: make(chan struct{}), frames: frames}
			names, err := healthModels(context.Background(), w, t.TempDir())
			if (err != nil) != tc.fail {
				t.Fatalf("unexpected outcome %v", err)
			}
			if !tc.fail && len(names) != 1 {
				t.Fatal("missing discovery")
			}
		})
	}
}
