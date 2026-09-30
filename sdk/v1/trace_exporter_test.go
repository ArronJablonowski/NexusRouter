package v1_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
)

func TestSDKTraceExporterOwnership(t *testing.T) {
	options, path := sdkToolOptions(t)
	c, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	appendTraceTask(t, path)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{}")
	}))
	defer server.Close()
	export := sdk.TraceExportOptions{Endpoint: server.URL + "/v1/traces", Limit: 1}
	for _, client := range []*sdk.Client{nil, {}} {
		if exporter, err := client.StartTraceExport(context.Background(), export, time.Second); err != sdk.ErrAdmission || exporter != nil {
			t.Fatal(exporter, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if exporter, err := c.StartTraceExport(ctx, export, time.Second); !errors.Is(err, context.Canceled) || exporter != nil {
		t.Fatal(exporter, err)
	}
	var absent *sdk.TraceExporter
	if absent.Close() != nil || absent.Health().Component != "trace_export" {
		t.Fatal("nil exporter is not inspectable")
	}
	exporter, err := c.StartTraceExport(context.Background(), export, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for exporter.Health().Status == "unknown" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if check := exporter.Health(); check.Status != "healthy" || check.Validate() != nil {
		t.Fatal(check)
	}
	if exporter.Close() != nil || exporter.Health().Status != "unavailable" {
		t.Fatal(exporter.Health())
	}
}
