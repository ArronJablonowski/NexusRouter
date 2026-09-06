package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/health"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/policy"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/resources"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

var ErrHealth = errors.New("health report unavailable")

func (s *Service) HealthReport(outer context.Context, supervisor health.Check) (report health.Report, err error) {
	defer func() {
		if recover() != nil {
			report = health.Report{}
			err = ErrHealth
		}
		if outer.Err() != nil {
			report = health.Report{}
			err = ErrHealth
		}
		if err == nil {
			report.Status, report.Ready = health.Outcome(report.Checks)
			if report.Validate() != nil {
				report = health.Report{}
				err = ErrHealth
			}
		}
	}()
	ctx, cancel := context.WithTimeout(outer, 5*time.Second)
	defer cancel()
	report = health.Report{Version: 1, CheckedAt: time.Now().UTC(), Status: "unavailable", Checks: []health.Check{{Component: "daemon", Status: "healthy", Code: "serving"}}}
	add := func(component, id, status, code string) {
		report.Checks = append(report.Checks, health.Check{Component: component, ID: id, Status: status, Code: code})
	}
	if s == nil {
		return health.Report{}, ErrHealth
	}
	if len(s.settings.Providers) > 64 || len(s.settings.Models) > 256 {
		add("provider", "configuration", "unavailable", "configuration_limit")
		add("database", "", "unknown", "unavailable")
		add("supervisor", "", "unknown", "supervisor_unavailable")
		add("resources", "host", "unknown", "metrics_unknown")
		return report, nil
	}
	secrets := []string{}
	providerKeys := make([]string, len(s.settings.Providers))
	if s.secret != nil {
		if value := s.secret("DARWIN_API_TOKEN"); value != "" {
			secrets = append(secrets, value)
		}
		for i, p := range s.settings.Providers {
			if p.APIKeyEnv != "" {
				if value := s.secret(p.APIKeyEnv); value != "" {
					providerKeys[i] = value
					secrets = append(secrets, value)
				}
			}
		}
	}
	if value := metricsExportSecret(s.settings, s.secret); value != "" {
		secrets = append(secrets, value)
	}
	usedIDs := map[string]bool{}
	safeID := func(component, id string, index int) string {
		if !sessions.ValidEventPageID(id) || strings.Contains(id, "/") {
			id = fmt.Sprintf("%s-%d", component, index)
		}
		for _, secret := range secrets {
			if strings.Contains(id, secret) {
				id = fmt.Sprintf("%s-%d", component, index)
				break
			}
		}
		for n := 0; usedIDs[component+"\x00"+id]; n++ {
			id = fmt.Sprintf("%s-%d-%d", component, index, n)
		}
		usedIDs[component+"\x00"+id] = true
		return id
	}
	db, dbErr := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	databaseHealthy := dbErr == nil
	if dbErr == nil {
		if db.Close() != nil {
			databaseHealthy = false
		}
	}
	if databaseHealthy {
		add("database", "", "healthy", "available")
	} else {
		add("database", "", "unavailable", "unavailable")
	}
	// Only accept a validated supervisor fact; never relay arbitrary caller text.
	if supervisor.Component != "supervisor" || supervisor.Validate() != nil {
		supervisor = health.Check{Component: "supervisor", Status: "unavailable", Code: "supervisor_unavailable"}
	}
	if supervisor.ID != "" {
		supervisor.ID = safeID("supervisor", supervisor.ID, 0)
	}
	report.Checks = append(report.Checks, supervisor)
	profile := s.profile
	if profile == nil {
		profile = resources.Profile
	}
	profileCtx, profileCancel := context.WithTimeout(ctx, 2*time.Second)
	snapshot, profileErr := profile(profileCtx)
	profileCancel()
	hostStatus, hostCode, gpuStatus, gpuCode := healthResources(snapshot, profileErr, s.settings.Hardware.MaxRAM, s.settings.Hardware.MaxVRAM)
	add("resources", "host", hostStatus, hostCode)
	add("resources", "gpu", gpuStatus, gpuCode)
	if hostStatus == "unknown" || profileErr != nil || snapshot.ThermalPressure == nil {
		add("resources", "thermal", "unknown", "metrics_unknown")
	} else if *snapshot.ThermalPressure {
		add("resources", "thermal", "degraded", "capacity_exhausted")
	} else {
		add("resources", "thermal", "healthy", "capacity_available")
	}
	enabled := make([]bool, len(s.settings.Models))
	policyBlocked := make([]bool, len(s.settings.Models))
	for i, m := range s.settings.Models {
		enabled[i] = (s.settings.Mode != "local_only" || m.Locality == "local") && (s.settings.Mode != "cloud_only" || m.Locality == "cloud")
		if enabled[i] {
			for _, p := range s.settings.Providers {
				if p.ID == m.Provider {
					transport, policyErr := policy.NewTransport(m.Locality == "local" || s.settings.Mode == "local_only", []string{p.Endpoint})
					if policyErr != nil {
						policyBlocked[i] = true
					} else {
						transport.CloseIdleConnections()
					}
					break
				}
			}
		}
	}
	type probe struct {
		enabled, local bool
		key            string
		names          []string
		err            error
		code           string
	}
	probes := make([]probe, len(s.settings.Providers))
	for i, p := range s.settings.Providers {
		probes[i].local = true
		for j, m := range s.settings.Models {
			if enabled[j] && !policyBlocked[j] && m.Provider == p.ID {
				probes[i].enabled = true
				if m.Locality != "local" {
					probes[i].local = false
				}
			}
		}
		probes[i].key = providerKeys[i]
		if p.APIKeyEnv != "" && probes[i].key == "" {
			probes[i].code = "credentials_missing"
		}
	}
	jobs := make(chan int, len(probes))
	for i := range probes {
		if probes[i].enabled && probes[i].code == "" {
			jobs <- i
		}
	}
	close(jobs)
	var workers sync.WaitGroup
	var panicked atomic.Bool
	for n := 0; n < 4; n++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() {
				if recover() != nil {
					panicked.Store(true)
				}
			}()
			for i := range jobs {
				p := s.settings.Providers[i]
				query, stop := context.WithTimeout(ctx, 2*time.Second)
				transport, err := policy.NewTransport(s.settings.Mode == "local_only" || probes[i].local, []string{p.Endpoint})
				if err == nil {
					var adapter providers.Provider
					adapter, err = providers.Build(query, s.providerFactory, providers.Connection{Version: 1, ID: p.ID, Endpoint: p.Endpoint, Kind: p.Kind, APIKey: probes[i].key, Transport: transport})
					if err == nil {
						probes[i].names, err = adapter.Models(query)
					}
					transport.CloseIdleConnections()
				}
				stop()
				probes[i].err = err
			}
		}()
	}
	workers.Wait()
	if panicked.Load() || outer.Err() != nil {
		return health.Report{}, ErrHealth
	}
	for i, p := range s.settings.Providers {
		status, code := "healthy", "available"
		if !probes[i].enabled {
			status, code = "disabled", "disabled_by_policy"
		} else if probes[i].code != "" {
			status, code = "unavailable", probes[i].code
		} else if probes[i].err != nil {
			status, code = "unavailable", "discovery_failed"
		}
		add("provider", safeID("provider", p.ID, i), status, code)
	}
	for i, m := range s.settings.Models {
		status, code := "unavailable", "model_missing"
		if !enabled[i] {
			status, code = "disabled", "disabled_by_policy"
		} else if policyBlocked[i] {
			status, code = "unavailable", "unavailable"
		} else if m.Locality == "local" && m.RAMBytes == 0 {
			status, code = "unavailable", "model_metadata_missing"
		} else {
			for j, p := range s.settings.Providers {
				if m.Provider != p.ID {
					continue
				}
				if probes[j].err != nil || probes[j].code != "" {
					code = "discovery_failed"
					continue
				}
				for _, name := range probes[j].names {
					if name == m.Model {
						status, code = "healthy", "available"
						break
					}
				}
			}
			modelGPUStatus := gpuStatus
			if m.GPUDevice != "" {
				modelGPUStatus = "unknown"
				if total, available, err := resources.DeviceMemory(snapshot, m.GPUDevice, time.Now(), 5*time.Second); err == nil {
					deviceSnapshot := snapshot
					deviceSnapshot.VRAMTotal, deviceSnapshot.VRAMAvailable = &total, &available
					_, _, modelGPUStatus, _ = healthResources(deviceSnapshot, profileErr, s.settings.Hardware.MaxRAM, s.settings.Hardware.MaxVRAM)
				}
			}
			if m.Locality == "local" && (hostStatus != "healthy" || m.VRAMBytes > 0 && modelGPUStatus != "healthy") {
				status, code = "unavailable", "capacity_exhausted"
			}
		}
		add("model", safeID("model", m.ID, i), status, code)
	}
	return report, nil
}

func healthResources(s resources.Snapshot, err error, ram, vram float64) (string, string, string, string) {
	validPercent := func(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) && n > 0 && n <= 100 }
	now := time.Now()
	if err != nil || s.Time.IsZero() || s.Time.After(now) || now.Sub(s.Time) > 5*time.Second || s.CPUs < 1 || s.TotalRAM == 0 || s.AvailableRAM > s.TotalRAM || !validPercent(ram) || !validPercent(vram) {
		return "unknown", "metrics_unknown", "unknown", "metrics_unknown"
	}
	host, code := "healthy", "capacity_available"
	if float64(s.TotalRAM-s.AvailableRAM) > float64(s.TotalRAM)*ram/100 || s.ThermalPressure != nil && *s.ThermalPressure {
		host, code = "degraded", "capacity_exhausted"
	}
	if s.VRAMTotal == nil || s.VRAMAvailable == nil {
		return host, code, "unknown", "metrics_unknown"
	}
	if *s.VRAMTotal == 0 || *s.VRAMAvailable > *s.VRAMTotal {
		return host, code, "unknown", "metrics_unknown"
	}
	if float64(*s.VRAMTotal-*s.VRAMAvailable) > float64(*s.VRAMTotal)*vram/100 {
		return host, code, "degraded", "capacity_exhausted"
	}
	return host, code, "healthy", "capacity_available"
}
