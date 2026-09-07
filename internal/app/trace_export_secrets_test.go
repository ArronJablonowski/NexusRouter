package app

import (
	"context"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
)

func TestTraceExportCredentialParticipatesInRedaction(t *testing.T) {
	cfg := config.Defaults()
	cfg.Telemetry.TraceExport = &config.TraceExport{APIKeyEnv: "TRACE_KEY"}
	lookups := 0
	resolve := func(name string) string {
		lookups++
		if name == "TRACE_KEY" {
			return "private-trace-token"
		}
		return ""
	}
	values := memorySecrets(cfg, resolve)
	found := false
	for _, value := range values {
		found = found || value == "private-trace-token"
	}
	if !found || lookups == 0 || traceExportSecret(cfg, nil) != "" {
		t.Fatal("trace credential omitted from redaction", values, lookups)
	}
	cfg.Telemetry.TraceExport = &config.TraceExport{}
	lookups = 0
	if traceExportSecret(cfg, resolve) != "" || lookups != 0 {
		t.Fatal("absent credential name was resolved")
	}
}

func TestTraceExportCredentialRejectedFromSubmission(t *testing.T) {
	s := submissionService(t)
	s.settings.Telemetry.TraceExport = &config.TraceExport{Enabled: false, APIKeyEnv: "TRACE_KEY"}
	s.secret = func(name string) string {
		if name == "TRACE_KEY" {
			return "private-trace-token"
		}
		return ""
	}
	if status, err := s.Submit(context.Background(), "0123456789abcdef", Request{ModelID: "model", Prompt: "private-trace-token"}); err == nil || status.ID != "" {
		t.Fatal("trace credential entered queued submission", status, err)
	}
}
