package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTraceExportDefaultsLayersAndRedaction(t *testing.T) {
	s, err := Load(Options{})
	if err != nil || s.Telemetry.TraceExport != nil {
		t.Fatal(s.Telemetry, err)
	}
	body, _ := json.Marshal(s)
	if strings.Contains(string(body), "trace_export") {
		t.Fatal("default fingerprint changed")
	}
	s, err = Load(Options{UserFile: file(t, "telemetry:\n  trace_export:\n    enabled: true\n    endpoint: https://collector.example/private-traces\n    interval: 2m\n    limit: 10\n"), ProjectFile: file(t, "telemetry:\n  trace_export:\n    enabled: false\n"), Env: map[string]string{"telemetry.trace_export.interval": "5s"}, Flags: map[string]string{"telemetry.trace_export.interval": "7s"}})
	if err != nil || s.Telemetry.TraceExport == nil || s.Telemetry.TraceExport.Enabled || s.Telemetry.TraceExport.Endpoint != "https://collector.example/private-traces" || s.Telemetry.TraceExport.Interval != "7s" || s.Telemetry.TraceExport.Limit != 10 {
		t.Fatal(s.Telemetry, err)
	}
	before, _ := json.Marshal(s)
	redacted, err := s.RedactedJSON()
	after, _ := json.Marshal(s)
	if err != nil || string(before) != string(after) || strings.Contains(string(redacted), "private-traces") {
		t.Fatal(string(redacted), err)
	}
	env := Environment([]string{"DARWIN__TELEMETRY__TRACE_EXPORT__ENABLED=true", "DARWIN__TELEMETRY__TRACE_EXPORT__ENDPOINT=http://localhost/v1/traces", "DARWIN__TELEMETRY__TRACE_EXPORT__API_KEY_ENV=TRACE_KEY", "DARWIN__TELEMETRY__TRACE_EXPORT__LIMIT=4"})
	s, err = Load(Options{Env: env})
	if err != nil || s.Telemetry.TraceExport == nil || !s.Telemetry.TraceExport.Enabled || s.Telemetry.TraceExport.APIKeyEnv != "TRACE_KEY" || s.Telemetry.TraceExport.Limit != 4 {
		t.Fatal(s.Telemetry, err)
	}
}

func TestTraceExportValidation(t *testing.T) {
	for _, field := range []string{"enabled: 'false'", "api_key_env: true", "endpoint: 17", "interval: 60", "limit: 1.5"} {
		if _, err := Load(Options{UserFile: file(t, "telemetry:\n  trace_export:\n    "+field+"\n")}); err == nil {
			t.Fatal("coerced trace field", field)
		}
	}
	for _, interval := range []string{"1s", "24h", ""} {
		if _, err := (TraceExport{Interval: interval}).IntervalDuration(); err != nil {
			t.Fatal(interval, err)
		}
	}
	for _, bad := range []TraceExport{{Enabled: true}, {Endpoint: "http://remote.example/v1/traces"}, {Endpoint: "https://user:secret@host/v1/traces"}, {Endpoint: "https://host/v1/traces?token=x"}, {Endpoint: "https://host/v1/traces", APIKeyEnv: "bad name"}, {Endpoint: "https://host/v1/traces", Interval: "999ms"}, {Endpoint: "https://host/v1/traces", Limit: 33}} {
		s := Defaults()
		s.Telemetry.TraceExport = &bad
		if s.Validate() == nil {
			t.Fatal("invalid trace config accepted", bad)
		}
	}
	s := Defaults()
	s.Mode = "local_only"
	s.Telemetry.TraceExport = &TraceExport{Enabled: true, Endpoint: "https://collector.example/v1/traces"}
	if s.Validate() == nil {
		t.Fatal("local-only remote trace collector accepted")
	}
	s.Telemetry.TraceExport = &TraceExport{Enabled: true, Endpoint: "http://127.0.0.1:4318/v1/traces", Interval: time.Second.String()}
	if s.Validate() != nil {
		t.Fatal("loopback trace collector rejected")
	}
	if _, err := Load(Options{Env: map[string]string{"telemetry.trace_export.unknown": "x"}}); err == nil {
		t.Fatal("unknown trace override accepted")
	}
}
