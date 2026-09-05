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

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/providers"
	"github.com/ArronJablonowski/DarwinRouter/resources"
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
