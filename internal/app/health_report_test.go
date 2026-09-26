package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/health"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/resources"
)

func healthySupervisor() health.Check {
	return health.Check{Component: "supervisor", Status: "healthy", Code: "supervisor_ok"}
}
func healthProfile(context.Context) (resources.Snapshot, error) {
	return resources.Snapshot{Time: time.Now().UTC(), CPUs: 4, TotalRAM: 1000, AvailableRAM: 900}, nil
}

func TestHealthReportReadOnlyCatalogAndSafeMetadata(t *testing.T) {
	var probes atomic.Int32
	p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes.Add(1)
		if r.Method != "GET" || r.URL.Path != "/api/tags" {
			t.Errorf("unexpected provider operation %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprintln(w, `{"models":[{"name":"fixture"},{"name":"unlisted-secret-response"}]}`)
	}))
	defer p.Close()
	s := submissionService(t)
	s.settings.Mode = "local_only"
	s.settings.Providers = []config.Provider{{ID: "provider-private-key", Kind: "ollama", Endpoint: p.URL}}
	s.settings.Models = []config.Model{{ID: "model-private-key", Provider: "provider-private-key", Model: "fixture", Locality: "local", RAMBytes: 1}}
	s.secret = func(string) string { return "private-key" }
	s.profile = healthProfile
	db, err := telemetry.Open(context.Background(), s.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	report, err := s.HealthReport(context.Background(), healthySupervisor())
	if err != nil || !report.Ready || report.Status != "degraded" || report.Validate() != nil || probes.Load() != 1 {
		t.Fatal(report, err, probes.Load())
	}
	body, _ := json.Marshal(report)
	for _, secret := range []string{"private-key", "unlisted-secret-response", p.URL, s.settings.Telemetry.Database} {
		if strings.Contains(string(body), secret) {
			t.Fatal("unsafe report", string(body))
		}
	}
	for _, check := range report.Checks {
		if check.Component == "resources" && check.ID == "gpu" && check.Status != "unknown" {
			t.Fatal("invented GPU health", check)
		}
	}
}

func TestHealthReportRequiresEnabledLocalRAMMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, locality, mode string
		ram                  uint64
		ready                bool
	}{
		{"missing local RAM", "local", "local_only", 0, false},
		{"known local RAM", "local", "local_only", 1, true},
		{"cloud RAM not required", "cloud", "cloud_only", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "GET" || r.URL.Path != "/api/tags" {
					t.Error("health executed provider work")
				}
				fmt.Fprintln(w, `{"models":[{"name":"fixture"}]}`)
			}))
			defer p.Close()
			s := submissionService(t)
			s.profile = healthProfile
			s.settings.Mode = tc.mode
			s.settings.Providers = []config.Provider{{ID: "provider", Kind: "ollama", Endpoint: p.URL}}
			s.settings.Models = []config.Model{{ID: "model", Provider: "provider", Model: "fixture", Locality: tc.locality, RAMBytes: tc.ram}}
			db, err := telemetry.Open(context.Background(), s.settings.Telemetry.Database)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			budget := s.budget
			report, err := s.HealthReport(context.Background(), healthySupervisor())
			if err != nil || report.Validate() != nil || report.Ready != tc.ready || calls.Load() != 1 || s.budget != budget {
				t.Fatal(report, err, calls.Load())
			}
			found := false
			for _, check := range report.Checks {
				if check.Component != "model" {
					continue
				}
				found = true
				if tc.ready {
					if check.Status != "healthy" || check.Code != "available" {
						t.Fatal(check)
					}
				} else if check.Status != "unavailable" || check.Code != "model_metadata_missing" {
					t.Fatal(check)
				}
			}
			if !found {
				t.Fatal("model observation missing")
			}
		})
	}
}

func TestHealthReportPolicyAndMissingDatabase(t *testing.T) {
	for _, mode := range []string{"local_only", "cloud_only"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprintln(w, `{"models":[]}`) }))
			defer p.Close()
			s := submissionService(t)
			s.profile = healthProfile
			s.settings.Mode = mode
			locality := "cloud"
			if mode == "cloud_only" {
				locality = "local"
			}
			s.settings.Providers = []config.Provider{{ID: "provider", Kind: "ollama", Endpoint: p.URL}}
			s.settings.Models = []config.Model{{ID: "model", Provider: "provider", Model: "fixture", Locality: locality}}
			report, err := s.HealthReport(context.Background(), healthySupervisor())
			if err != nil || report.Ready || calls.Load() != 0 {
				t.Fatal(report, err, calls.Load())
			}
			for _, check := range report.Checks {
				if check.Component == "model" && (check.Status != "disabled" || check.Code != "disabled_by_policy") {
					t.Fatal("missing metadata overrode mode classification", check)
				}
			}
			if _, err := os.Stat(s.settings.Telemetry.Database); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("health created database", err)
			}
		})
	}
}

func TestHealthReportBoundsCancellationAndPanic(t *testing.T) {
	s := submissionService(t)
	s.profile = healthProfile
	s.settings.Providers = make([]config.Provider, 65)
	s.secret = func(string) string { t.Fatal("oversized configuration consulted credentials"); return "" }
	report, err := s.HealthReport(context.Background(), healthySupervisor())
	if err != nil || report.Ready || report.Validate() != nil {
		t.Fatal(report, err)
	}
	s.settings.Providers = nil
	s.secret = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.HealthReport(ctx, healthySupervisor()); !errors.Is(err, ErrHealth) {
		t.Fatal(err)
	}
	s.profile = func(context.Context) (resources.Snapshot, error) { panic("private profiler detail") }
	if _, err := s.HealthReport(context.Background(), healthySupervisor()); !errors.Is(err, ErrHealth) {
		t.Fatal(err)
	}
}

func TestHealthResourceThresholds(t *testing.T) {
	s, _ := healthProfile(context.Background())
	s.AvailableRAM = 1
	host, _, gpu, _ := healthResources(s, nil, 80, 85)
	if host != "degraded" || gpu != "unknown" {
		t.Fatal(host, gpu)
	}
	s.Time = time.Now().Add(-time.Minute)
	host, _, _, _ = healthResources(s, nil, 80, 85)
	if host != "unknown" {
		t.Fatal(host)
	}
}

func TestHealthReportBoundsParallelDiscovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var active, maximum, total atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); n > old; old = maximum.Load() {
			if maximum.CompareAndSwap(old, n) {
				break
			}
		}
		if total.Add(1) == 4 {
			close(started)
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		case <-ctx.Done():
			return
		}
		fmt.Fprintln(w, `{"models":[{"name":"fixture"}]}`)
	}))
	defer func() { cancel(); p.Close() }()
	s := submissionService(t)
	s.profile = healthProfile
	s.settings.Mode = "local_only"
	for i := 0; i < 8; i++ {
		id := fmt.Sprint("p", i)
		s.settings.Providers = append(s.settings.Providers, config.Provider{ID: id, Kind: "ollama", Endpoint: p.URL})
		s.settings.Models = append(s.settings.Models, config.Model{ID: id, Provider: id, Model: "fixture", Locality: "local"})
	}
	done := make(chan error, 1)
	go func() { _, err := s.HealthReport(ctx, healthySupervisor()); done <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("four probes never started")
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("probes did not finish")
	}
	if maximum.Load() != 4 || total.Load() != 8 {
		t.Fatal(maximum.Load(), total.Load())
	}
}

func TestHealthReportProbeTimeoutIsTypedFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	stopped := make(chan struct{})
	p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(stopped)
		select {
		case <-r.Context().Done():
		case <-ctx.Done():
		}
	}))
	defer func() { cancel(); p.Close() }()
	s := submissionService(t)
	s.profile = healthProfile
	s.settings.Mode = "local_only"
	s.settings.Providers = []config.Provider{{ID: "provider", Kind: "ollama", Endpoint: p.URL}}
	s.settings.Models = []config.Model{{ID: "model", Provider: "provider", Model: "fixture", Locality: "local"}}
	report, err := s.HealthReport(ctx, healthySupervisor())
	if err != nil || report.Ready || report.Validate() != nil {
		t.Fatal(report, err)
	}
	found := false
	for _, check := range report.Checks {
		if check.Component == "provider" && check.Code == "discovery_failed" {
			found = true
		}
	}
	if !found {
		t.Fatal(report)
	}
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("catalog request not canceled")
	}
}

func TestHealthReportCodexDiscovery(t *testing.T) {
	for _, tc := range []struct {
		mode   string
		fail   bool
		status string
	}{
		{"hybrid", false, "healthy"}, {"cloud_only", false, "healthy"}, {"local_only", false, "disabled"}, {"hybrid", true, "unavailable"},
	} {
		t.Run(fmt.Sprint(tc), func(t *testing.T) {
			s := submissionService(t)
			s.settings.Mode = tc.mode
			s.settings.Providers = []config.Provider{{ID: "codex", Kind: "codex_app_server", Executable: "/fixture/codex"}}
			s.settings.Models = []config.Model{{ID: "cloud", Provider: "codex", Model: "gpt-5.6-sol", Locality: "cloud"}}
			s.profile = healthProfile
			calls := 0
			s.codexHealthModels = func(ctx context.Context, executable string) ([]string, error) {
				calls++
				if executable != "/fixture/codex" {
					t.Error(executable)
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Error("unbounded discovery")
				}
				if tc.fail {
					return nil, errors.New("private failure")
				}
				return []string{"gpt-5.6-sol"}, nil
			}
			report, err := s.HealthReport(context.Background(), healthySupervisor())
			if err != nil {
				t.Fatal(err)
			}
			for _, check := range report.Checks {
				if check.Component == "model" || check.Component == "provider" {
					if check.Status != tc.status {
						t.Fatal(check)
					}
				}
			}
			if (calls == 0) != (tc.mode == "local_only") {
				t.Fatal(calls)
			}
		})
	}
}
