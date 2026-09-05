package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"darwinrouter/resources"
	"darwinrouter/routing"
)

func TestAutomaticPressureClassification(t *testing.T) {
	for _, mode := range []string{"capacity", "metadata", "profile", "policy"} {
		t.Run(mode, func(t *testing.T) {
			s, cfg := autoFixture(t)
			r := Request{ModelID: "auto", Prompt: "hello"}
			switch mode {
			case "capacity":
				release, err := s.reserveExplicit(context.Background(), cfg.Models[0])
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			case "metadata":
				for i := range s.settings.Models {
					s.settings.Models[i].RAMBytes = 0
				}
			case "profile":
				s.profile = func(context.Context) (resources.Snapshot, error) {
					return resources.Snapshot{}, errors.New("profile failed")
				}
			case "policy":
				r.Capabilities = []string{"not_supported"}
			}
			out, err := s.runAuto(context.Background(), r)
			if out.TaskID != "" || !errors.Is(err, routing.ErrNoRoute) || errors.Is(err, resources.ErrCapacity) != (mode == "capacity") {
				t.Fatal(out, err)
			}
		})
	}
}

func TestAutomaticPressureWaitReplansAfterRelease(t *testing.T) {
	s, cfg := autoFixture(t)
	s.settings.Hardware.LocalPressurePolicy = "wait"
	s.settings.Hardware.LocalQueueTimeout = "2s"
	release, err := s.reserveExplicit(context.Background(), cfg.Models[0])
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	profiled := make(chan struct{}, 4)
	s.profile = func(context.Context) (resources.Snapshot, error) {
		select {
		case profiled <- struct{}{}:
		default:
		}
		return resources.Snapshot{Time: time.Now(), TotalRAM: 1000, AvailableRAM: 1000}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.Run(ctx, Request{ModelID: "auto", Prompt: "hello"}); done <- err }()
	// A second profile while capacity is held proves a fresh planning retry.
	for i := 0; i < 2; i++ {
		select {
		case <-profiled:
		case err := <-done:
			t.Fatal("did not wait", err)
		case <-ctx.Done():
			t.Fatal("planning not retried")
		}
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("release did not admit")
	}
}

func TestAutomaticPressureCloudAlternativeAndLocalRequired(t *testing.T) {
	for _, localRequired := range []bool{false, true} {
		t.Run(fmt.Sprint(localRequired), func(t *testing.T) {
			s, cfg := autoFixture(t)
			s.settings.Mode = "hybrid"
			s.settings.Models[1].Locality = "cloud"
			release, err := s.reserveExplicit(context.Background(), cfg.Models[0])
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			out, err := s.runAuto(context.Background(), Request{ModelID: "auto", Prompt: "hello", LocalRequired: localRequired})
			if localRequired {
				if out.TaskID != "" || !errors.Is(err, resources.ErrCapacity) {
					t.Fatal(out, err)
				}
			} else if err != nil || out.Text != "z" {
				t.Fatal("cloud alternative unavailable", out, err)
			}
		})
	}
}

func TestAutomaticPressureDoesNotRetryProviderFailure(t *testing.T) {
	s, _ := autoFixture(t)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			fmt.Fprintln(w, `{"models":[{"name":"a"}]}`)
			return
		}
		calls.Add(1)
		w.WriteHeader(503)
	}))
	defer provider.Close()
	s.settings.Providers[0].Endpoint = provider.URL
	s.settings.Models = s.settings.Models[:1]
	s.settings.Hardware.LocalPressurePolicy = "wait"
	s.settings.Hardware.LocalQueueTimeout = "1s"
	out, err := s.Run(context.Background(), Request{ModelID: "auto", Prompt: "hello"})
	if err == nil || out.TaskID == "" || calls.Load() != 1 || errors.Is(err, resources.ErrCapacity) {
		t.Fatal(out, err, calls.Load())
	}
}

func TestAutomaticCanceledAdmissionContextDoesNotDispatch(t *testing.T) {
	s, _ := autoFixture(t)
	queue, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := s.runAuto(context.Background(), Request{ModelID: "auto", Prompt: "hello", admissionContext: queue})
	if !errors.Is(err, ErrAdmission) || out.TaskID != "" {
		t.Fatal(out, err)
	}
}
