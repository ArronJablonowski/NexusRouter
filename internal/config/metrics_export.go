package config

import (
	"errors"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/metrics"
	"go.yaml.in/yaml/v3"
)

type MetricsExport struct {
	Enabled   bool   `yaml:"enabled" json:"enabled"`
	Endpoint  string `yaml:"endpoint" json:"endpoint"`
	APIKeyEnv string `yaml:"api_key_env" json:"api_key_env"`
	Interval  string `yaml:"interval" json:"interval"`
}

func (m MetricsExport) IntervalDuration() (time.Duration, error) {
	if m.Interval == "" {
		return time.Minute, nil
	}
	d, err := time.ParseDuration(m.Interval)
	if err != nil || d < time.Second || d > 24*time.Hour {
		return 0, errors.New("invalid metrics export interval")
	}
	return d, nil
}

func (m *MetricsExport) validate(mode string) error {
	if m == nil {
		return nil
	}
	if _, err := m.IntervalDuration(); err != nil {
		return err
	}
	endpoint := m.Endpoint
	if endpoint == "" {
		if m.Enabled {
			return errors.New("metrics export endpoint required")
		}
		endpoint = "https://collector.invalid/v1/metrics" // Validate only a supplied credential name when disabled.
	}
	if (metrics.ExportOptions{Endpoint: endpoint, APIKeyEnv: m.APIKeyEnv}).Validate() != nil {
		return errors.New("invalid metrics export configuration")
	}
	if m.Enabled && mode == "local_only" {
		u, err := url.Parse(endpoint)
		if err != nil {
			return errors.New("invalid metrics export configuration")
		}
		if !strings.EqualFold(u.Hostname(), "localhost") {
			ip, err := netip.ParseAddr(u.Hostname())
			if err != nil || ip.Zone() != "" || !ip.Unmap().IsLoopback() {
				return errors.New("local-only metrics export requires loopback")
			}
		}
	}
	return nil
}

// A nil optional block stays absent unless an explicit scalar override selects
// it. Seed all its scalar types before merging the existing file-layer values.
func seedMetricsExportOverrides(root *yaml.Node, overrides map[string]string) error {
	needed := false
	for key := range overrides {
		if strings.HasPrefix(key, "telemetry.metrics_export.") {
			needed = true
			break
		}
	}
	if !needed {
		return nil
	}
	data, err := yaml.Marshal(struct {
		Telemetry struct {
			MetricsExport MetricsExport `yaml:"metrics_export"`
		} `yaml:"telemetry"`
	}{})
	if err != nil {
		return errors.New("invalid metrics export configuration")
	}
	seed, err := parse(data)
	if err != nil {
		return err
	}
	merge(seed, root)
	*root = *seed
	return nil
}
