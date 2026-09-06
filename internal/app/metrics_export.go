package app

import (
	"bytes"
	"context"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/metrics"
	"github.com/ArronJablonowski/DarwinRouter/policy"
)

// ExportMetrics explicitly sends one current aggregate snapshot. It never starts
// a scheduler, dispatches a model, writes storage, follows redirects or retries.
// An error may occur after the collector accepted data; no durable delivery or
// exactly-once acknowledgement is implied.
func (s *Service) ExportMetrics(ctx context.Context, options metrics.ExportOptions) error {
	return s.exportMetrics(ctx, options, nil)
}

func (s *Service) exportMetrics(ctx context.Context, options metrics.ExportOptions, authorized func() bool) (err error) {
	defer func() {
		if recover() != nil || err != nil {
			err = metrics.ErrExport
		}
	}()
	if s == nil || ctx == nil || ctx.Err() != nil || s.settings.Validate() != nil || s.settings.Telemetry.OTEL || options.Validate() != nil {
		return metrics.ErrExport
	}
	if authorized != nil && !authorized() {
		return metrics.ErrExport
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	mode, database := s.settings.Mode, s.settings.Telemetry.Database
	transport, err := policy.NewTransport(metricsExportPinned(mode, options.Endpoint), []string{options.Endpoint})
	if err != nil {
		return err
	}
	defer transport.CloseIdleConnections()
	key := ""
	if options.APIKeyEnv != "" {
		if s.secret == nil {
			return metrics.ErrExport
		}
		key = s.secret(options.APIKeyEnv)
		if !metricsExportToken(key) {
			return metrics.ErrExport
		}
	}
	snapshot, err := s.Metrics(ctx)
	if err != nil {
		return err
	}
	body, err := metrics.MarshalOTLP(snapshot)
	if err != nil || ctx.Err() != nil || s.settings.Mode != mode || s.settings.Telemetry.Database != database {
		return metrics.ErrExport
	}
	// A rotating credential is not silently replaced after source inspection.
	if options.APIKeyEnv != "" && s.secret(options.APIKeyEnv) != key {
		return metrics.ErrExport
	}
	if ctx.Err() != nil || s.settings.Mode != mode || s.settings.Telemetry.Database != database || s.settings.Validate() != nil || s.settings.Telemetry.OTEL {
		return metrics.ErrExport
	}
	if authorized != nil && !authorized() {
		return metrics.ErrExport
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, options.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.GetBody = nil
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	media, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || mediaErr != nil || media != "application/json" {
		return metrics.ErrExport
	}
	acknowledgement, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(acknowledgement) > 64<<10 || ctx.Err() != nil || !metricsExportAcknowledged(acknowledgement) {
		return metrics.ErrExport
	}
	return nil
}

// Pin all permitted plaintext destinations and localhost even in hybrid mode.
// Otherwise host resolver configuration could move a bearer request off loopback.
func metricsExportPinned(mode, endpoint string) bool {
	u, err := url.Parse(endpoint)
	return mode == "local_only" || err != nil || u.Scheme == "http" || strings.EqualFold(u.Hostname(), "localhost")
}

func metricsExportToken(value string) bool {
	if len(value) == 0 || len(value) > 8192 {
		return false
	}
	for _, c := range []byte(value) {
		if c <= 32 || c >= 127 {
			return false
		}
	}
	return true
}
