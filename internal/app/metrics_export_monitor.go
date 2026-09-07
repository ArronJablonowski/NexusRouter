package app

import (
	"context"
	"sync"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/health"
	"github.com/ArronJablonowski/DarwinRouter/metrics"
	"github.com/ArronJablonowski/DarwinRouter/policy"
)

// MetricsExporter owns sequential, ephemeral scheduling, not durable delivery.
// Each attempt reads a fresh snapshot; no failed body is retried. Its owner must
// Close it. Secret callbacks must cooperate with cancellation and be safe for
// concurrent use by other application operations.
type MetricsExporter struct {
	mu          sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
	err         error
	status      string
	code        string
	stepStarted time.Time
}

// StartMetricsExport starts immediately, then waits interval after each attempt
// completes. Slow collectors cannot accumulate ticks or overlap requests within
// this handle. Separate handles/processes are independent exporters.
func StartMetricsExport(ctx context.Context, s *Service, options metrics.ExportOptions, interval time.Duration) (*MetricsExporter, error) {
	return startMetricsExport(ctx, s, options, interval, nil)
}

// StartConfiguredMetricsExport is the daemon's explicit configuration boundary.
// An absent or disabled setting returns an inert handle without reading storage
// or resolving credentials. telemetry.opentelemetry_enabled is a compatibility
// alias for enabling this metrics exporter; it does not enable runtime traces.
func StartConfiguredMetricsExport(ctx context.Context, s *Service) (*MetricsExporter, error) {
	if ctx == nil || ctx.Err() != nil || s == nil || s.settings.Validate() != nil {
		return nil, metrics.ErrExport
	}
	cfg := s.settings.Telemetry.MetricsExport
	if cfg == nil || (!cfg.Enabled && !s.settings.Telemetry.OTEL) {
		m := &MetricsExporter{done: make(chan struct{}), status: "disabled", code: "disabled_by_policy"}
		close(m.done)
		return m, nil
	}
	expected, expectedOTEL := *cfg, s.settings.Telemetry.OTEL
	interval, err := expected.IntervalDuration()
	if err != nil {
		return nil, metrics.ErrExport
	}
	authorized := func() bool {
		current := s.settings.Telemetry.MetricsExport
		return current != nil && *current == expected && s.settings.Telemetry.OTEL == expectedOTEL
	}
	return startMetricsExport(ctx, s, metrics.ExportOptions{Endpoint: expected.Endpoint, APIKeyEnv: expected.APIKeyEnv}, interval, authorized)
}

func startMetricsExport(ctx context.Context, s *Service, options metrics.ExportOptions, interval time.Duration, authorized func() bool) (*MetricsExporter, error) {
	if ctx == nil || ctx.Err() != nil || s == nil || s.settings.Validate() != nil || options.Validate() != nil || interval < time.Second || interval > 24*time.Hour {
		return nil, metrics.ErrExport
	}
	// Validate destination authority before starting, without DNS or a connection.
	transport, err := policy.NewTransport(metricsExportPinned(s.settings.Mode, options.Endpoint), []string{options.Endpoint})
	if err != nil {
		return nil, metrics.ErrExport
	}
	transport.CloseIdleConnections()
	ctx, cancel := context.WithCancel(ctx)
	m := &MetricsExporter{cancel: cancel, done: make(chan struct{}), status: "unknown", code: "supervisor_starting"}
	go m.run(ctx, interval, func(ctx context.Context) error { return s.exportMetrics(ctx, options, authorized) })
	return m, nil
}

func (m *MetricsExporter) run(ctx context.Context, interval time.Duration, step func(context.Context) error) {
	defer close(m.done)
	defer func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if recover() != nil {
			m.err = metrics.ErrExport
		}
		m.stepStarted = time.Time{}
		m.status, m.code = "unavailable", "supervisor_stopped"
	}()
	for ctx.Err() == nil {
		m.mu.Lock()
		m.stepStarted = time.Now()
		m.mu.Unlock()
		err := step(ctx)
		if ctx.Err() != nil {
			return
		}
		m.mu.Lock()
		m.stepStarted = time.Time{}
		m.err = nil
		m.status, m.code = "healthy", "supervisor_ok"
		if err != nil {
			m.err = metrics.ErrExport
			m.status, m.code = "degraded", "supervisor_error"
		}
		m.mu.Unlock()
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Close cancels and joins the current attempt. It returns the last completed
// attempt's generic error, if any; cancellation alone is not a delivery error.
func (m *MetricsExporter) Close() error {
	if m == nil || m.done == nil {
		return nil
	}
	if m.cancel != nil {
		m.cancel()
	}
	<-m.done
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

func (m *MetricsExporter) Health() health.Check {
	out := health.Check{Component: "metrics_export", Status: "unknown", Code: "supervisor_starting"}
	if m == nil || m.done == nil {
		return out
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out.Status, out.Code = m.status, m.code
	if !m.stepStarted.IsZero() && time.Since(m.stepStarted) > 10*time.Second {
		out.Status, out.Code = "degraded", "supervisor_stalled"
	}
	return out
}
