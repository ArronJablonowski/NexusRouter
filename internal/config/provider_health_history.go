package config

import (
	"errors"
	"time"
)

// ProviderHealthHistory controls the daemon-owned, local provider-health
// sampler. Records contain only validated health enums and configured safe IDs;
// they never contain endpoints, credentials, prompts, or model output.
type ProviderHealthHistory struct {
	Enabled  bool   `yaml:"enabled" json:"enabled"`
	Interval string `yaml:"interval" json:"interval"`
	Retain   int    `yaml:"retain" json:"retain"`
}

func (h ProviderHealthHistory) IntervalDuration() (time.Duration, error) {
	if h.Interval == "" {
		return 30 * time.Second, nil
	}
	d, err := time.ParseDuration(h.Interval)
	if err != nil || d < 5*time.Second || d > time.Hour {
		return 0, errors.New("invalid provider health history interval")
	}
	return d, nil
}

func (h ProviderHealthHistory) validate() error {
	if _, err := h.IntervalDuration(); err != nil {
		return err
	}
	if h.Retain < 1 || h.Retain > 100000 {
		return errors.New("invalid provider health history retention")
	}
	return nil
}
