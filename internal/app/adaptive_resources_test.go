package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/resources"
)

func TestAdaptiveResourceServiceConstruction(t *testing.T) {
	const gib = uint64(1 << 30)
	for _, tc := range []struct {
		name, setting       string
		headroom            uint64
		cpus, workers, want int
	}{
		{"low", "auto", 8 * gib, 8, 4, 1},
		{"middle", "auto", 32 * gib, 8, 4, 2},
		{"high", "auto", 96 * gib, 8, 4, 4},
		{"worker_ceiling", "auto", 96 * gib, 8, 2, 2},
		{"cpu_ceiling", "auto", 96 * gib, 2, 4, 2},
		{"cpu_unknown", "auto", 96 * gib, 0, 4, 1},
		{"fixed_one", "1", 96 * gib, 8, 4, 1},
		{"fixed_three", "3", 8 * gib, 8, 4, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, cfg := autoFixture(t)
			cfg.Hardware.Concurrent = tc.setting
			cfg.Hardware.MaxRAM = 100
			cfg.Workers.Max = tc.workers
			s, err := NewService(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			s.profile = func(context.Context) (resources.Snapshot, error) {
				return resources.Snapshot{Time: time.Now(), CPUs: tc.cpus, TotalRAM: 128 * gib, AvailableRAM: tc.headroom}, nil
			}
			var releases []func()
			defer func() {
				for _, release := range releases {
					release()
				}
			}()
			for i := 0; i < tc.want; i++ {
				release, err := s.reserveExplicit(context.Background(), cfg.Models[0])
				if err != nil {
					t.Fatal("premature capacity denial", i, err)
				}
				releases = append(releases, release)
			}
			if release, err := s.reserveExplicit(context.Background(), cfg.Models[0]); err == nil {
				release()
				t.Fatal("exceeded adaptive or fixed capacity")
			}
		})
	}
}

func TestAdaptiveResourcesMissingProfileFailsClosed(t *testing.T) {
	_, cfg := autoFixture(t)
	cfg.Hardware.Concurrent = "auto"
	cfg.Workers.Max = 4
	s, err := NewService(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.profile = func(context.Context) (resources.Snapshot, error) { return resources.Snapshot{}, nil }
	if _, err := s.Run(context.Background(), Request{ModelID: "a", Prompt: "hello"}); !errors.Is(err, ErrAdmission) {
		t.Fatal(err)
	}
}

func TestAdaptiveExplicitAndAutomaticShareCapacity(t *testing.T) {
	for _, firstAuto := range []bool{false, true} {
		t.Run(fmt.Sprint(firstAuto), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			entered := make(chan struct{}, 2)
			release := make(chan struct{})
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					fmt.Fprintln(w, `{"models":[{"name":"a"},{"name":"z"}]}`)
					return
				}
				_, _ = io.Copy(io.Discard, r.Body)
				select {
				case entered <- struct{}{}:
				default:
					t.Error("provider dispatched beyond capacity")
				}
				select {
				case <-release:
					fmt.Fprintln(w, `{"message":{"content":"answer"},"done":true,"done_reason":"stop"}`)
				case <-r.Context().Done():
				case <-ctx.Done():
				}
			}))
			defer func() { cancel(); provider.Close() }()
			_, cfg := autoFixture(t)
			cfg.Providers[0].Endpoint = provider.URL
			cfg.Workers.Max = 3
			cfg.Hardware.Concurrent = "auto"
			cfg.Hardware.MaxRAM = 100
			s, err := NewService(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			s.profile = func(context.Context) (resources.Snapshot, error) {
				return resources.Snapshot{Time: time.Now(), CPUs: 8, TotalRAM: 64 << 30, AvailableRAM: 32 << 30}, nil
			}
			done := make(chan error, 2)
			first := Request{ModelID: "a", Prompt: "first"}
			if firstAuto {
				first.ModelID = "auto"
			}
			go func() { _, err := s.Run(ctx, first); done <- err }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("first model did not enter")
			}
			go func() { _, err := s.Run(ctx, Request{ModelID: "a", Prompt: "second"}); done <- err }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("second model did not enter; possible double reservation")
			}
			if _, err := s.Run(ctx, Request{ModelID: "a", Prompt: "third"}); !errors.Is(err, ErrAdmission) {
				t.Fatal("third exceeded adaptive capacity", err)
			}
			close(release)
			for i := 0; i < 2; i++ {
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal("execution did not finish")
				}
			}
			if _, err := s.Run(ctx, Request{ModelID: "a", Prompt: "after release"}); err != nil {
				t.Fatal("reservation leaked", err)
			}
		})
	}
}
