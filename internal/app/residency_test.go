package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/resources"
	"github.com/ArronJablonowski/NexusRouter/runtime"
)

type residencyFixture struct {
	mu                         sync.Mutex
	resident                   string
	unloads, streams, profiles int
	uncertain, noRecovery      bool
	svc                        *Service
}

func managedResidencyFixture(t *testing.T) (*Service, *residencyFixture) {
	t.Helper()
	svc, cfg := autoFixture(t)
	fixture := &residencyFixture{resident: "z", svc: svc}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Lifecycle network must never retain the global resource lock.
		if r.URL.Path == "/api/ps" || r.URL.Path == "/api/generate" {
			if !svc.mu.TryLock() {
				t.Error("provider network under resource lock")
			} else {
				svc.mu.Unlock()
			}
		}
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"z"}]}`)
		case "/api/ps":
			items := []providers.ResidentModel{}
			if fixture.resident != "" {
				items = append(items, providers.ResidentModel{Name: fixture.resident, Model: fixture.resident, Size: 500, SizeVRAM: 0, Digest: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Hour)})
			}
			json.NewEncoder(w).Encode(map[string]any{"models": items})
		case "/api/generate":
			fixture.unloads++
			if fixture.uncertain {
				http.Error(w, "fixture", 500)
				return
			}
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["model"] != "z" || body["keep_alive"] != float64(0) {
				t.Error("wrong unload")
			}
			fixture.resident = ""
			json.NewEncoder(w).Encode(map[string]any{"model": "z", "created_at": time.Now().UTC(), "response": "", "done": true, "done_reason": "unload"})
		case "/api/chat":
			fixture.streams++
			fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
		default:
			t.Error("unexpected provider path")
		}
	}))
	t.Cleanup(server.Close)
	cfg.Providers[0].Endpoint = server.URL
	cfg.Providers[0].ManageResidency = true
	var err error
	svc, err = NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture.svc = svc
	svc.profile = func(context.Context) (resources.Snapshot, error) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		fixture.profiles++
		available := uint64(50)
		if fixture.resident == "" && !fixture.noRecovery {
			available = 1000
		}
		return resources.Snapshot{Time: time.Now(), TotalRAM: 1000, AvailableRAM: available, CPUs: 4}, nil
	}
	return svc, fixture
}

func TestRuntimeHostAdmissionRejectsManagedResidencyBeforeProfileOrProvider(t *testing.T) {
	svc, fixture := managedResidencyFixture(t)
	ctx := context.Background()
	store, err := telemetry.Open(ctx, svc.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	request, err := withRuntimeHostAdmission(Request{ModelID: "a", Prompt: "do not dispatch"}, runtimeHostAdmission{
		taskID: "managed-host-task", sessionID: "managed-host-session", parentTaskID: "managed-host-parent",
		workerID: "managed-host-worker", store: store,
		commitFirst: func(ctx context.Context, event runtime.Event) error { return store.Append(ctx, 0, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Run(ctx, request); !errors.Is(err, ErrAdmission) {
		t.Fatalf("managed host route admitted: %v", err)
	}
	fixture.mu.Lock()
	profiles, unloads, streams := fixture.profiles, fixture.unloads, fixture.streams
	fixture.mu.Unlock()
	if profiles != 0 || unloads != 0 || streams != 0 {
		t.Fatalf("managed host rejection followed side-effect path: profiles=%d unloads=%d streams=%d", profiles, unloads, streams)
	}
	events, readErr := store.Read(ctx, "managed-host-task", 0, 10)
	if readErr != nil || len(events) != 0 {
		t.Fatalf("managed host rejection created a task: events=%+v err=%v", events, readErr)
	}
}

func TestManagedResidencyConfirmsUnloadAndReprofiles(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(fmt.Sprint(automatic), func(t *testing.T) {
			svc, f := managedResidencyFixture(t)
			model := "a"
			if automatic {
				model = "auto"
			}
			out, err := svc.Run(context.Background(), Request{ModelID: model, Prompt: "hello"})
			if err != nil || out.Text != "answer" {
				t.Fatal(out, err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.unloads != 1 || f.streams != 1 || f.profiles < 2 {
				t.Fatal(f.unloads, f.streams, f.profiles)
			}
		})
	}
}

func TestManagedResidencyUncertaintyNeverRetriesUnload(t *testing.T) {
	svc, f := managedResidencyFixture(t)
	f.uncertain = true
	for range 2 {
		if _, err := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "hello"}); !errors.Is(err, resources.ErrCapacity) {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	if f.unloads != 1 || f.streams != 0 {
		t.Fatal("unconfirmed unload retried or dispatched", f.unloads, f.streams)
	}
	// A different configured name for the same digest is not proof of absence.
	f.resident = "a"
	f.mu.Unlock()
	if _, err := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "hello"}); !errors.Is(err, resources.ErrCapacity) {
		t.Fatal("digest alias cleared uncertainty", err)
	}
}

func TestManagedResidencyUnknownOrUnrecoveredPressureDenies(t *testing.T) {
	for _, mode := range []string{"unknown", "no_recovery", "custom_factory"} {
		t.Run(mode, func(t *testing.T) {
			svc, f := managedResidencyFixture(t)
			switch mode {
			case "unknown":
				f.resident = "foreign"
			case "no_recovery":
				f.noRecovery = true
			case "custom_factory":
				svc.providerFactory = residencyRejectFactory{}
			}
			if _, err := svc.Run(context.Background(), Request{ModelID: "a", Prompt: "hello"}); err == nil {
				t.Fatal("unsafe route admitted")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.streams != 0 || (mode != "no_recovery" && f.unloads != 0) {
				t.Fatal("unsafe provider mutation")
			}
		})
	}
}

type residencyRejectFactory struct{}

func (residencyRejectFactory) Build(context.Context, providers.Connection) (providers.Provider, error) {
	return nil, ErrAdmission
}

func TestManagedResidencyActiveModelCannotBeUnloaded(t *testing.T) {
	svc, f := managedResidencyFixture(t)
	f.resident = ""
	var first, second config.Model
	for _, m := range svc.settings.Models {
		if m.ID == "z" {
			first = m
		} else {
			second = m
		}
	}
	release, err := svc.reserveExplicit(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.resident = "z"
	f.mu.Unlock()
	if _, err := svc.reserveExplicit(context.Background(), second); !errors.Is(err, resources.ErrCapacity) {
		t.Fatal("active model switched", err)
	}
	f.mu.Lock()
	if f.unloads != 0 {
		t.Error("active model unloaded")
	}
	f.mu.Unlock()
	release()
	release()
	next, err := svc.reserveExplicit(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	next()
}

func TestManagedResidencyMissingSourceAndCredentialsDoNotUnload(t *testing.T) {
	for _, mode := range []string{"source", "credentials"} {
		t.Run(mode, func(t *testing.T) {
			svc, f := managedResidencyFixture(t)
			request := Request{ModelID: "a", Prompt: "hello"}
			if mode == "source" {
				request.ContinueTaskID = "missing-source"
			} else {
				svc.settings.Providers[0].APIKeyEnv = "MISSING_RESIDENCY_KEY"
			}
			if _, err := svc.Run(context.Background(), request); err == nil {
				t.Fatal("invalid input accepted")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.unloads != 0 || f.streams != 0 {
				t.Fatal("denied source mutated residency")
			}
		})
	}
}

func expandedResidencyFixture(t *testing.T) (*Service, *residencyFixture) {
	t.Helper()
	svc, fixture := managedResidencyFixture(t)
	for i := range svc.settings.Models {
		svc.settings.Models[i].RAMBytes = 12 << 30
		svc.settings.Models[i].ContextTokens = 131072
	}
	svc.profile = func(context.Context) (resources.Snapshot, error) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		fixture.profiles++
		// The 80% RAM budget leaves 20 GiB before an idle model is
		// unloaded, more than the fixed 16 GiB low-memory tier.
		available := uint64(40 << 30)
		if fixture.resident == "" && !fixture.noRecovery {
			available = 64 << 30
		}
		return resources.Snapshot{Time: time.Now(), TotalRAM: 100 << 30, AvailableRAM: available, CPUs: 4}, nil
	}
	return svc, fixture
}

func TestManagedResidencyReclaimsForExpandedContextAboveLowMemoryTier(t *testing.T) {
	for _, coordinated := range []bool{false, true} {
		t.Run(fmt.Sprint(coordinated), func(t *testing.T) {
			svc, fixture := expandedResidencyFixture(t)
			if coordinated {
				installReservationFixture(t, svc, newReservationCoordinatorFixture())
			}
			result, err := svc.Run(context.Background(), Request{ModelID: "a", ContextTokens: 65536, Prompt: "hello"})
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if coordinated {
				if !errors.Is(err, resources.ErrCapacity) || fixture.unloads != 0 || fixture.streams != 0 {
					t.Fatalf("coordinated path gained peer-unaware unload authority: %+v %v, unloads=%d streams=%d", result, err, fixture.unloads, fixture.streams)
				}
			} else if err != nil || result.Text != "answer" || fixture.unloads != 1 || fixture.streams != 1 || fixture.profiles < 2 {
				t.Fatalf("24 GiB context allocation did not reclaim idle memory: %+v %v, unloads=%d streams=%d", result, err, fixture.unloads, fixture.streams)
			}
		})
	}
}

func TestManagedResidencyDoesNotUnloadForNonMemoryBlockers(t *testing.T) {
	for _, mode := range []string{"thermal", "swap", "concurrency", "invalid", "unrecovered"} {
		t.Run(mode, func(t *testing.T) {
			svc, fixture := expandedResidencyFixture(t)
			profile := svc.profile
			svc.profile = func(ctx context.Context) (resources.Snapshot, error) {
				snapshot, err := profile(ctx)
				pressure := true
				switch mode {
				case "thermal":
					snapshot.ThermalPressure = &pressure
				case "swap":
					snapshot.SwapPressure = &pressure
				case "invalid":
					snapshot.CPUs = -1
				}
				return snapshot, err
			}
			if mode == "concurrency" {
				for range 2 {
					now := time.Now()
					release, err := svc.budget.Reserve(resources.Snapshot{Time: now, TotalRAM: 100 << 30, AvailableRAM: 100 << 30, CPUs: 4}, resources.Need{RAM: 1 << 30}, now)
					if err != nil {
						t.Fatal(err)
					}
					defer release()
				}
			}
			if mode == "unrecovered" {
				fixture.noRecovery = true
			}
			_, err := svc.Run(context.Background(), Request{ModelID: "a", ContextTokens: 65536, Prompt: "hello"})
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			wantUnloads := 0
			if mode == "unrecovered" {
				wantUnloads = 1
			}
			if err == nil || fixture.unloads != wantUnloads || fixture.streams != 0 {
				t.Fatalf("denied route mutated residency or dispatched: %v, unloads=%d streams=%d", err, fixture.unloads, fixture.streams)
			}
		})
	}
}
