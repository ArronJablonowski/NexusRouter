package app

import (
	"context"
	"sync"
	"time"

	"github.com/ArronJablonowski/NexusRouter/health"
	"github.com/ArronJablonowski/NexusRouter/policy"
	"github.com/ArronJablonowski/NexusRouter/traces"
)

// TraceExporter owns sequential ephemeral scheduling. Delivery is best effort,
// non-durable and non-retrying. The owner must Close the handle.
type TraceExporter struct {
	mu          sync.Mutex
	cancel      context.CancelFunc
	done        chan struct{}
	err         error
	status      string
	code        string
	stepStarted time.Time
}

func StartTraceExport(ctx context.Context, s *Service, options traces.ExportOptions, interval time.Duration) (*TraceExporter, error) {
	return startTraceExport(ctx, s, options, interval, nil)
}

// StartConfiguredTraceExport is the daemon configuration boundary. Disabled
// configuration returns an inert inspectable handle without reading storage,
// resolving credentials or contacting the collector.
func StartConfiguredTraceExport(ctx context.Context, s *Service) (*TraceExporter, error) {
	if ctx == nil || ctx.Err() != nil || s == nil || s.settings.Validate() != nil {
		return nil, traces.ErrExport
	}
	cfg := s.settings.Telemetry.TraceExport
	if cfg == nil || !cfg.Enabled {
		exporter := &TraceExporter{done: make(chan struct{}), status: "disabled", code: "disabled_by_policy"}
		close(exporter.done)
		return exporter, nil
	}
	expected := *cfg
	interval, err := expected.IntervalDuration()
	if err != nil {
		return nil, traces.ErrExport
	}
	authorized := func() bool {
		current := s.settings.Telemetry.TraceExport
		return current != nil && *current == expected
	}
	options := traces.ExportOptions{Endpoint: expected.Endpoint, APIKeyEnv: expected.APIKeyEnv, Limit: expected.Limit}
	return startTraceExport(ctx, s, options, interval, authorized)
}

func startTraceExport(ctx context.Context, s *Service, options traces.ExportOptions, interval time.Duration, authorized func() bool) (*TraceExporter, error) {
	if ctx == nil || ctx.Err() != nil || s == nil || s.settings.Validate() != nil || options.Validate() != nil || interval < time.Second || interval > 24*time.Hour {
		return nil, traces.ErrExport
	}
	// Validate destination authority without DNS or a connection.
	transport, err := policy.NewTransport(metricsExportPinned(s.settings.Mode, options.Endpoint), []string{options.Endpoint}, s.settings.DNSAudit())
	if err != nil {
		return nil, traces.ErrExport
	}
	transport.CloseIdleConnections()
	ctx, cancel := context.WithCancel(ctx)
	exporter := &TraceExporter{cancel: cancel, done: make(chan struct{}), status: "unknown", code: "supervisor_starting"}
	go exporter.run(ctx, interval, func(ctx context.Context) error { return s.exportTraces(ctx, options, authorized) })
	return exporter, nil
}

func (e *TraceExporter) run(ctx context.Context, interval time.Duration, step func(context.Context) error) {
	defer close(e.done)
	defer func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if recover() != nil {
			e.err = traces.ErrExport
		}
		e.stepStarted = time.Time{}
		e.status, e.code = "unavailable", "supervisor_stopped"
	}()
	for ctx.Err() == nil {
		e.mu.Lock()
		e.stepStarted = time.Now()
		e.mu.Unlock()
		err := step(ctx)
		if ctx.Err() != nil {
			return
		}
		e.mu.Lock()
		e.stepStarted = time.Time{}
		e.err = nil
		e.status, e.code = "healthy", "supervisor_ok"
		if err != nil {
			e.err = traces.ErrExport
			e.status, e.code = "degraded", "supervisor_error"
		}
		e.mu.Unlock()
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (e *TraceExporter) Close() error {
	if e == nil || e.done == nil {
		return nil
	}
	if e.cancel != nil {
		e.cancel()
	}
	<-e.done
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}

func (e *TraceExporter) Health() health.Check {
	out := health.Check{Component: "trace_export", Status: "unknown", Code: "supervisor_starting"}
	if e == nil || e.done == nil {
		return out
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out.Status, out.Code = e.status, e.code
	if !e.stepStarted.IsZero() && time.Since(e.stepStarted) > 10*time.Second {
		out.Status, out.Code = "degraded", "supervisor_stalled"
	}
	return out
}
