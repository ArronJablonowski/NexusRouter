package v1_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	sdk "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
)

func TestSDKMetricsExportAdmission(t *testing.T) {
	options, path := sdkToolOptions(t)
	c, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	export := sdk.MetricsExportOptions{Endpoint: "http://127.0.0.1:1/v1/metrics"}
	for _, client := range []*sdk.Client{nil, {}} {
		if err := client.ExportMetrics(ctx, export); err != sdk.ErrAdmission {
			t.Fatal(err)
		}
	}
	if err := c.ExportMetrics(nil, export); err != sdk.ErrAdmission {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := c.ExportMetrics(canceled, export); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := c.ExportMetrics(ctx, export); err == nil {
		t.Fatal("missing database accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("created database", err)
	}
}

func TestSDKMetricsExportOneShot(t *testing.T) {
	options, path := sdkToolOptions(t)
	options.LookupSecret = func(name string) string {
		if name == "METRICS_KEY" {
			return "fixture-token"
		}
		return ""
	}
	c, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	db, err := telemetry.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || r.URL.Path != "/v1/metrics" || r.Header.Get("Authorization") != "Bearer fixture-token" || !bytes.Contains(body, []byte("resourceMetrics")) || bytes.Contains(body, []byte("fixture-token")) {
			t.Error("incorrect collector request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{}")
	}))
	defer server.Close()
	if err := c.ExportMetrics(context.Background(), sdk.MetricsExportOptions{Endpoint: server.URL + "/v1/metrics", APIKeyEnv: "METRICS_KEY"}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("not exactly one export")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("database changed")
	}
}
