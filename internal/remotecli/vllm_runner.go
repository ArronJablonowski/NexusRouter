package remotecli

import (
	"context"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Only the fixed operator-installed unit is controlled; no browser command or
// model path is executed. systemd owns the process beyond the request lifetime.
func vllmController(cfg config.Settings, service *app.Service) func(context.Context, string) (remote.RunnerStatus, error) {
	var mu sync.Mutex
	return func(ctx context.Context, action string) (remote.RunnerStatus, error) {
		mu.Lock()
		defer mu.Unlock()
		out := remote.RunnerStatus{Version: 1, Enabled: cfg.VLLM.Enabled, ModelID: cfg.VLLM.ModelID, State: "disabled"}
		if !out.Enabled {
			return out, nil
		}
		if action != "status" && action != "start" && action != "stop" {
			return out, remote.ErrInvalid
		}
		query, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if action != "status" {
			if service.RunnerIdle(query) != nil {
				return out, remote.ErrConflict
			}
			if action == "start" {
				snap, err := resources.Profile(query)
				if err != nil || snap.Time.IsZero() || time.Since(snap.Time) > 5*time.Second || snap.AvailableRAM < cfg.VLLM.MinimumFreeRAMBytes || snap.ThermalPressure == nil || *snap.ThermalPressure {
					return out, remote.ErrUnavailable
				}
			}
			if exec.CommandContext(query, "systemctl", "--user", action, "--no-block", "nexusrouter-vllm.service").Run() != nil {
				return out, remote.ErrUnavailable
			}
		}
		raw, err := exec.CommandContext(query, "systemctl", "--user", "show", "--property=ActiveState", "--value", "nexusrouter-vllm.service").Output()
		if err != nil {
			return out, remote.ErrUnavailable
		}
		out.State = strings.TrimSpace(string(raw))
		if out.Validate() != nil {
			return out, remote.ErrUnavailable
		}
		return out, nil
	}
}
