package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMetricsExportDefaultsAndRedaction(t *testing.T) {
	s, err := Load(Options{})
	if err != nil || s.Telemetry.MetricsExport != nil {
		t.Fatal(s.Telemetry, err)
	}
	body, _ := json.Marshal(s)
	if strings.Contains(string(body), "metrics_export") {
		t.Fatal("default fingerprint changed")
	}
	d, err := (MetricsExport{}).IntervalDuration()
	if err != nil || d != time.Minute {
		t.Fatal(d, err)
	}
	m := &MetricsExport{Enabled: true, Endpoint: "https://collector.example/private-path", APIKeyEnv: "COLLECTOR_KEY", Interval: "2m"}
	s.Telemetry.MetricsExport = m
	before, _ := json.Marshal(s)
	redacted, err := s.RedactedJSON()
	after, _ := json.Marshal(s)
	if err != nil || string(before) != string(after) || m.Endpoint != "https://collector.example/private-path" || strings.Contains(string(redacted), "private-path") || !strings.Contains(string(redacted), "COLLECTOR_KEY") {
		t.Fatal(string(redacted), err)
	}
}

func TestMetricsExportLayersAndExplicitFalse(t *testing.T) {
	s, err := Load(Options{UserFile: file(t, "telemetry:\n  metrics_export:\n    enabled: true\n    endpoint: https://collector.example/v1/metrics\n    interval: 2m\n"), ProjectFile: file(t, "telemetry:\n  metrics_export:\n    enabled: false\n"), Env: map[string]string{"telemetry.metrics_export.interval": "5s"}, Flags: map[string]string{"telemetry.metrics_export.interval": "7s"}})
	if err != nil || s.Telemetry.MetricsExport == nil || s.Telemetry.MetricsExport.Enabled || s.Telemetry.MetricsExport.Interval != "7s" || s.Telemetry.MetricsExport.Endpoint != "https://collector.example/v1/metrics" {
		t.Fatal(s.Telemetry, err)
	}
	env := Environment([]string{"DARWIN__TELEMETRY__METRICS_EXPORT__ENABLED=true", "DARWIN__TELEMETRY__METRICS_EXPORT__ENDPOINT=http://localhost/v1/metrics", "DARWIN__TELEMETRY__METRICS_EXPORT__API_KEY_ENV=COLLECTOR_KEY"})
	s, err = Load(Options{Env: env})
	if err != nil || s.Telemetry.MetricsExport == nil || !s.Telemetry.MetricsExport.Enabled || s.Telemetry.MetricsExport.APIKeyEnv != "COLLECTOR_KEY" {
		t.Fatal(s.Telemetry, err)
	}
	if _, err = Load(Options{Env: map[string]string{"telemetry.metrics_export.unknown": "x"}}); err == nil {
		t.Fatal("unknown override accepted")
	}
	if _, err = Load(Options{Env: map[string]string{"telemetry.metrics_export.enabled": "not-bool"}, Flags: map[string]string{"telemetry.metrics_export.enabled": "false"}}); err == nil {
		t.Fatal("shadowed malformed boolean accepted")
	}
}

func TestMetricsExportValidation(t *testing.T) {
	for _, field := range []string{"enabled: 'false'", "api_key_env: true", "endpoint: 17", "interval: 60"} {
		if _, err := Load(Options{UserFile: file(t, "telemetry:\n  metrics_export:\n    "+field+"\n")}); err == nil {
			t.Fatal("coerced typed configuration", field)
		}
	}
	for _, enabled := range []bool{false, true} {
		for _, bad := range []MetricsExport{{Endpoint: "http://remote.example/v1/metrics"}, {Endpoint: "https://user:secret@host/v1/metrics"}, {Endpoint: "https://host/v1/metrics?token=x"}, {APIKeyEnv: "key with spaces"}, {Interval: "999ms"}, {Interval: "24h1ns"}, {Interval: "bogus"}} {
			s := Defaults()
			bad.Enabled = enabled
			s.Telemetry.MetricsExport = &bad
			if s.Validate() == nil {
				t.Fatal("invalid settings accepted", bad)
			}
		}
	}
	for _, endpoint := range []string{"http://localhost/v1/metrics", "https://LOCALHOST/v1/metrics", "http://127.0.0.2/v1/metrics", "http://[::1]/v1/metrics", "https://[::ffff:127.0.0.1]/v1/metrics"} {
		s := Defaults()
		s.Mode = "local_only"
		s.Telemetry.MetricsExport = &MetricsExport{Enabled: true, Endpoint: endpoint, Interval: "1s"}
		if s.Validate() != nil {
			t.Fatal("loopback denied", endpoint)
		}
	}
	s := Defaults()
	s.Mode = "local_only"
	s.Telemetry.MetricsExport = &MetricsExport{Enabled: true, Endpoint: "https://collector.example/v1/metrics"}
	if s.Validate() == nil {
		t.Fatal("local mode remote allowed")
	}
	s.Telemetry.MetricsExport.Enabled = false
	if s.Validate() != nil {
		t.Fatal("disabled valid remote configuration denied")
	}
	for _, interval := range []string{"1s", "24h", ""} {
		if _, err := (MetricsExport{Interval: interval}).IntervalDuration(); err != nil {
			t.Fatal(interval, err)
		}
	}
	s = Defaults()
	s.Telemetry.MetricsExport = &MetricsExport{Enabled: true}
	if s.Validate() == nil {
		t.Fatal("missing endpoint")
	}
	s.Telemetry.MetricsExport = &MetricsExport{}
	if s.Validate() != nil {
		t.Fatal("disabled empty block")
	}
}
