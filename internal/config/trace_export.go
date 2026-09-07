package config

import (
	"errors"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/traces"
	"go.yaml.in/yaml/v3"
)

type TraceExport struct {
	Enabled   bool   `yaml:"enabled" json:"enabled"`
	Endpoint  string `yaml:"endpoint" json:"endpoint"`
	APIKeyEnv string `yaml:"api_key_env" json:"api_key_env"`
	Interval  string `yaml:"interval" json:"interval"`
	Limit     int    `yaml:"limit" json:"limit"`
}

func (t TraceExport) IntervalDuration() (time.Duration, error) {
	if t.Interval == "" {
		return time.Minute, nil
	}
	d, err := time.ParseDuration(t.Interval)
	if err != nil || d < time.Second || d > 24*time.Hour {
		return 0, errors.New("invalid trace export interval")
	}
	return d, nil
}

func (t *TraceExport) validate(mode string) error {
	if t == nil {
		return nil
	}
	if _, err := t.IntervalDuration(); err != nil {
		return err
	}
	endpoint := t.Endpoint
	if endpoint == "" {
		if t.Enabled {
			return errors.New("trace export endpoint required")
		}
		endpoint = "https://collector.invalid/v1/traces"
	}
	options := traces.ExportOptions{Endpoint: endpoint, APIKeyEnv: t.APIKeyEnv, Limit: t.Limit}
	if options.Validate() != nil {
		return errors.New("invalid trace export configuration")
	}
	if t.Enabled && mode == "local_only" {
		u, err := url.Parse(endpoint)
		if err != nil {
			return errors.New("invalid trace export configuration")
		}
		if !strings.EqualFold(u.Hostname(), "localhost") {
			ip, err := netip.ParseAddr(u.Hostname())
			if err != nil || ip.Zone() != "" || !ip.Unmap().IsLoopback() {
				return errors.New("local-only trace export requires loopback")
			}
		}
	}
	return nil
}

func seedTraceExportOverrides(root *yaml.Node, overrides map[string]string) error {
	needed := false
	for key := range overrides {
		if strings.HasPrefix(key, "telemetry.trace_export.") {
			needed = true
			break
		}
	}
	if !needed {
		return nil
	}
	data, err := yaml.Marshal(struct {
		Telemetry struct {
			TraceExport TraceExport `yaml:"trace_export"`
		} `yaml:"telemetry"`
	}{})
	if err != nil {
		return errors.New("invalid trace export configuration")
	}
	seed, err := parse(data)
	if err != nil {
		return err
	}
	merge(seed, root)
	*root = *seed
	return nil
}
