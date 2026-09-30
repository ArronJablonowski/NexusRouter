package v1_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
)

func TestSDKMetricsExporterOwnership(t *testing.T) {
	options, path := sdkToolOptions(t)
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
	entered, exited := make(chan struct{}, 1), make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		entered <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-release:
		}
		exited <- struct{}{}
	}))
	defer server.Close()
	defer close(release)
	opts := sdk.MetricsExportOptions{Endpoint: server.URL + "/v1/metrics"}
	for _, client := range []*sdk.Client{nil, {}} {
		if _, err := client.StartMetricsExport(context.Background(), opts, time.Second); err != sdk.ErrAdmission {
			t.Fatal(err)
		}
	}
	if _, err := c.StartMetricsExport(nil, opts, time.Second); err != sdk.ErrAdmission {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.StartMetricsExport(ctx, opts, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, interval := range []time.Duration{0, -time.Second, 25 * time.Hour} {
		if e, err := c.StartMetricsExport(context.Background(), opts, interval); err == nil {
			_ = e.Close()
			t.Fatal("invalid interval accepted")
		}
	}
	var absent *sdk.MetricsExporter
	if absent.Close() != nil || absent.Health().Validate() != nil {
		t.Fatal("nil handle contract")
	}
	exporter, err := c.StartMetricsExport(context.Background(), opts, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer exporter.Close()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no immediate export")
	}
	if check := exporter.Health(); check.Component != "metrics_export" || check.Validate() != nil {
		t.Fatal(check)
	}
	done := make(chan error, 1)
	go func() { done <- exporter.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not cancel and join")
	}
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("request not canceled")
	}
	if exporter.Close() != nil || exporter.Health().Validate() != nil {
		t.Fatal("closed handle contract")
	}
}
