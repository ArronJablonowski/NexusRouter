package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/policy"
	"github.com/ArronJablonowski/DarwinRouter/traces"
)

var ErrTraces = errors.New("traces unavailable")

// TraceSnapshot reconstructs recent task lifecycles without creating or
// migrating storage and without exposing their durable identities/content.
func (s *Service) TraceSnapshot(ctx context.Context, limit int) (traces.Snapshot, error) {
	if s == nil || ctx == nil || limit < 1 || limit > traces.MaxTraces {
		return traces.Snapshot{}, ErrTraces
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return traces.Snapshot{}, ErrTraces
	}
	defer db.Close()
	snapshot, err := db.Traces(ctx, limit)
	if err != nil || snapshot.Validate() != nil {
		return traces.Snapshot{}, ErrTraces
	}
	return snapshot, nil
}

// ExportTraces explicitly sends one bounded recent-task trace snapshot. It
// starts no scheduler, follows no redirect, retries nothing and mutates no
// DarwinRouter state. Collector acknowledgement can still be lost.
func (s *Service) ExportTraces(ctx context.Context, options traces.ExportOptions) (err error) {
	return s.exportTraces(ctx, options, nil)
}

func (s *Service) exportTraces(ctx context.Context, options traces.ExportOptions, authorized func() bool) (err error) {
	defer func() {
		if recover() != nil || err != nil {
			err = traces.ErrExport
		}
	}()
	if s == nil || ctx == nil || ctx.Err() != nil || s.settings.Validate() != nil || options.Validate() != nil {
		return traces.ErrExport
	}
	if authorized != nil && !authorized() {
		return traces.ErrExport
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
			return traces.ErrExport
		}
		key = s.secret(options.APIKeyEnv)
		if !metricsExportToken(key) {
			return traces.ErrExport
		}
	}
	snapshot, err := s.TraceSnapshot(ctx, options.TraceLimit())
	if err != nil {
		return err
	}
	body, err := traces.MarshalOTLP(snapshot)
	if err != nil || ctx.Err() != nil || s.settings.Mode != mode || s.settings.Telemetry.Database != database {
		return traces.ErrExport
	}
	if options.APIKeyEnv != "" && s.secret(options.APIKeyEnv) != key {
		return traces.ErrExport
	}
	if ctx.Err() != nil || s.settings.Mode != mode || s.settings.Telemetry.Database != database || s.settings.Validate() != nil {
		return traces.ErrExport
	}
	if authorized != nil && !authorized() {
		return traces.ErrExport
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
		return traces.ErrExport
	}
	ack, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(ack) > 64<<10 || ctx.Err() != nil || !metricsExportAcknowledged(ack) {
		return traces.ErrExport
	}
	return nil
}
