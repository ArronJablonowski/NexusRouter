package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/resources"
)

func TestExplicitResourcesRejectBeforeStorage(t *testing.T) {
	for _, mode := range []string{"missing", "error", "panic", "stale", "invalid", "cancel", "input", "thermal", "ram", "vram_unknown", "unified_vram"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := autoFixture(t)
			request := Request{ModelID: "a", Prompt: "hello"}
			ctx := context.Background()
			switch mode {
			case "missing":
				s.settings.Models[0].RAMBytes = 0
			case "error":
				s.profile = func(context.Context) (resources.Snapshot, error) { return resources.Snapshot{}, errors.New("private") }
			case "panic":
				s.profile = func(context.Context) (resources.Snapshot, error) { panic("private") }
			case "stale":
				s.profile = func(context.Context) (resources.Snapshot, error) {
					return resources.Snapshot{Time: time.Now().Add(-time.Minute), TotalRAM: 1000, AvailableRAM: 1000}, nil
				}
			case "invalid":
				s.profile = func(context.Context) (resources.Snapshot, error) {
					return resources.Snapshot{Time: time.Now(), TotalRAM: 1, AvailableRAM: 2}, nil
				}
			case "thermal", "ram", "vram_unknown", "unified_vram":
				if mode == "vram_unknown" || mode == "unified_vram" {
					s.settings.Models[0].VRAMBytes = 1
				}
				s.profile = func(context.Context) (resources.Snapshot, error) {
					snapshot := resources.Snapshot{Time: time.Now(), TotalRAM: 1000, AvailableRAM: 1000}
					switch mode {
					case "thermal":
						pressure := true
						snapshot.ThermalPressure = &pressure
					case "ram":
						snapshot.AvailableRAM = 1
					case "unified_vram":
						snapshot.UnifiedMemory = true
					}
					return snapshot, nil
				}
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "input":
				request.Prompt = ""
				s.profile = func(context.Context) (resources.Snapshot, error) {
					t.Error("profiled invalid input")
					return resources.Snapshot{}, nil
				}
			}
			if _, err := s.runExplicit(ctx, request); !errors.Is(err, ErrAdmission) {
				t.Fatalf("error %v", err)
			}
			if _, err := os.Stat(s.settings.Telemetry.Database); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("storage created %v", err)
			}
			// A panic must not leave the service mutex locked or leak a reservation.
			s.profile = func(context.Context) (resources.Snapshot, error) {
				return resources.Snapshot{Time: time.Now(), TotalRAM: 1000, AvailableRAM: 1000}, nil
			}
			s.settings.Models[0].RAMBytes = 100
			s.settings.Models[0].VRAMBytes = 0
			if _, err := s.runExplicit(context.Background(), Request{ModelID: "a", Prompt: "retry"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExplicitResourceReleaseAfterDispatchedCancellation(t *testing.T) {
	s, _ := autoFixture(t)
	var calls atomic.Int32
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			close(entered)
			<-r.Context().Done()
			return
		}
		fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	s.settings.Providers[0].Endpoint = server.URL
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := Request{ModelID: "a", Prompt: "hello"}
	done := make(chan error, 1)
	go func() { _, err := s.Run(ctx, request); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("provider not dispatched")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled task succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not finish")
	}
	next, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if _, err := s.Run(next, request); err != nil {
		t.Fatalf("cancellation leaked reservation: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("provider calls %d", calls.Load())
	}
}

func TestExplicitAutomaticShareResourceCapacity(t *testing.T) {
	for _, firstAuto := range []bool{false, true} {
		t.Run(fmt.Sprint(firstAuto), func(t *testing.T) {
			s, _ := autoFixture(t)
			s.settings.Workers.Max = 2
			s.execution = make(chan struct{}, 2)
			entered, release := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"z"}]}`)
					return
				}
				select {
				case <-entered:
				default:
					close(entered)
				}
				select {
				case <-release:
					fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
				case <-r.Context().Done():
				}
			}))
			defer server.Close()
			s.settings.Providers[0].Endpoint = server.URL
			first, second := Request{ModelID: "a", Prompt: "first"}, Request{ModelID: "auto", Prompt: "second"}
			if firstAuto {
				first.ModelID = "auto"
				second.ModelID = "a"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := s.Run(ctx, first); done <- err }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("initial execution denied (possible double reservation)")
			}
			if _, err := s.Run(ctx, second); err == nil {
				t.Fatal("capacity bypass")
			}
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if _, err := s.Run(ctx, second); err != nil {
				t.Fatalf("reservation leaked %v", err)
			}
		})
	}
}

func TestExplicitCloudSkipsResourceProfile(t *testing.T) {
	s, _ := autoFixture(t)
	s.settings.Mode = "cloud_only"
	for i := range s.settings.Models {
		s.settings.Models[i].Locality = "cloud"
		s.settings.Models[i].RAMBytes = 0
	}
	s.profile = func(context.Context) (resources.Snapshot, error) {
		t.Fatal("cloud profiled")
		return resources.Snapshot{}, nil
	}
	if _, err := s.runExplicit(context.Background(), Request{ModelID: "a", Prompt: "hello"}); err != nil {
		t.Fatal(err)
	}
}

func TestExplicitResourceReleaseAfterProviderFailure(t *testing.T) {
	s, _ := autoFixture(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
	}))
	defer server.Close()
	s.settings.Providers[0].Endpoint = server.URL
	request := Request{ModelID: "a", Prompt: "hello"}
	if _, err := s.Run(context.Background(), request); err == nil {
		t.Fatal("expected provider failure")
	}
	if _, err := s.Run(context.Background(), request); err != nil {
		t.Fatalf("failure leaked reservation: %v", err)
	}
}
