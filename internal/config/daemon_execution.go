package config

import (
	"errors"
	"time"
)

// ExecutionDuration bounds each dispatched submission independently of caller
// disconnects. An omitted setting preserves the historical five-minute limit.
func (d Daemon) ExecutionDuration() (time.Duration, error) {
	if d.ExecutionTimeout == "" {
		return 5 * time.Minute, nil
	}
	timeout, err := Duration(d.ExecutionTimeout)
	if err != nil || timeout < 100*time.Millisecond || timeout > 30*time.Minute {
		return 0, errors.New("daemon execution timeout must be between 100ms and 30m")
	}
	return timeout, nil
}
