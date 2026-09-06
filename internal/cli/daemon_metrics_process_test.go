package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/daemon"
	"github.com/ArronJablonowski/DarwinRouter/health"
)

// Uses only the owned child and collector from the lifecycle fixture. No task
// is dispatched: /health exercises the actual request-readiness hook.
func qualifyDaemonMetricsFailure(t *testing.T, ctx context.Context, client *http.Client, address, token string, failing *atomic.Bool, status func(string) (daemon.Status, error)) {
	t.Helper()
	get := func(path string) (int, []byte) {
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		if err != nil || bytes.Contains(body, []byte("private-collector-detail")) {
			t.Fatal("health leaked collector details", err)
		}
		return response.StatusCode, body
	}
	for _, state := range []struct {
		fail             bool
		daemon, exporter string
	}{{true, "degraded", "degraded"}, {false, "ready", "healthy"}} {
		failing.Store(state.fail)
		deadline := time.NewTimer(8 * time.Second)
		tick := time.NewTicker(25 * time.Millisecond)
		matched := false
		for !matched {
			select {
			case <-deadline.C:
				tick.Stop()
				t.Fatal("exporter status did not converge", state.daemon)
			case <-ctx.Done():
				tick.Stop()
				deadline.Stop()
				t.Fatal(ctx.Err())
			case <-tick.C:
				s, err := status("status")
				matched = err == nil && s.State == state.daemon
			}
		}
		tick.Stop()
		deadline.Stop()
		if code, _ := get("/health"); code != http.StatusOK {
			t.Fatal("export failure blocked request readiness", code)
		}
		_, body := get("/v1/health")
		var report health.Report
		if json.Unmarshal(body, &report) != nil || report.Validate() != nil {
			t.Fatal("invalid health report")
		}
		found := false
		base := make([]health.Check, 0, len(report.Checks))
		for _, check := range report.Checks {
			if check.Component == "metrics_export" {
				found = check.Status == state.exporter
			} else {
				base = append(base, check)
			}
		}
		baseStatus, ready := health.Outcome(base)
		// This lifecycle fixture has no configured models, so aggregate model
		// readiness may already be unavailable; export must not change it.
		if !found || report.Ready != ready || state.fail && baseStatus == "healthy" && report.Status != "degraded" {
			t.Fatal("supplemental health mismatch", report)
		}
	}
}
