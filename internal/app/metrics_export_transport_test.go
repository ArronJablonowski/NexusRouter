package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/metrics"
)

func TestMetricsExportPinnedPolicy(t *testing.T) {
	for _, mode := range []string{"local_only", "hybrid", "cloud_only"} {
		for _, endpoint := range []string{"http://localhost/v1/metrics", "https://LOCALHOST/v1/metrics", "http://127.0.0.1/v1/metrics", "http://[::1]/v1/metrics", "https://collector.example/v1/metrics"} {
			want := mode == "local_only" || !strings.Contains(endpoint, "collector.example")
			if got := metricsExportPinned(mode, endpoint); got != want {
				t.Errorf("%s %s: pinned=%v, want %v", mode, endpoint, got, want)
			}
		}
	}
}

func TestMetricsExportHybridLocalhost(t *testing.T) {
	s := metricsExportFixture(t)
	s.settings.Mode = "hybrid"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	defer server.Close()
	endpoint := strings.Replace(server.URL, "127.0.0.1", "localhost", 1) + "/v1/metrics"
	if err := s.ExportMetrics(context.Background(), metrics.ExportOptions{Endpoint: endpoint}); err != nil {
		t.Fatal(err)
	}
}
