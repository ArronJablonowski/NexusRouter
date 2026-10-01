package remote

import (
	"context"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
	"testing"
	"time"
)

func TestSDKInfoScopesBeforeObservation(t *testing.T) {
	cost := 0.0
	b := &SDKBackend{Client: &sdk.Client{}, Models: []Model{{ID: "allowed", Local: true, Capabilities: []string{"chat"}, EstimatedCost: &cost}, {ID: "other", Local: true}, {ID: "cloud", Local: false}}}
	calls := 0
	b.Observe = func(_ context.Context, models []Model) ([]ModelObservation, *ResourceObservation, error) {
		calls++
		if len(models) != 1 || models[0].ID != "allowed" {
			t.Fatal("unscoped observation", models)
		}
		return []ModelObservation{{State: "present", CheckedAt: time.Now().UTC()}}, &ResourceObservation{State: "unknown", CheckedAt: time.Now().UTC()}, nil
	}
	info, err := b.InfoFor(context.Background(), []string{"allowed", "cloud"}, false)
	if err != nil || len(info.Models) != 1 || info.Models[0].Observation.State != "present" || calls != 1 {
		t.Fatal(info, err)
	}
	info.Models[0].Capabilities[0] = "mutated"
	if b.Models[0].Capabilities[0] != "chat" {
		t.Fatal("catalogue alias")
	}
	b.Observe = func(context.Context, []Model) ([]ModelObservation, *ResourceObservation, error) { return nil, nil, nil }
	if _, err = b.InfoFor(context.Background(), []string{"allowed"}, false); err == nil {
		t.Fatal("missing observations accepted")
	}
	b.Observe = func(context.Context, []Model) ([]ModelObservation, *ResourceObservation, error) {
		return []ModelObservation{{State: "present"}}, nil, nil
	}
	if _, err = b.InfoFor(context.Background(), []string{"allowed"}, false); err == nil {
		t.Fatal("missing timestamp accepted")
	}
}

func TestAuthenticatedInfoUsesScopedObserver(t *testing.T) {
	f := setup(t)
	b := &SDKBackend{Client: &sdk.Client{}, Models: []Model{{ID: "chat", Local: true}, {ID: "hidden", Local: true}, {ID: "cloud", Local: false}}, Available: func(context.Context) bool { return true }}
	b.Observe = func(_ context.Context, models []Model) ([]ModelObservation, *ResourceObservation, error) {
		if len(models) != 1 || models[0].ID != "chat" {
			return nil, nil, ErrDenied
		}
		return []ModelObservation{{State: "present", CheckedAt: time.Now().UTC()}}, nil, nil
	}
	f.server.backend = b
	info, err := f.client.Info(context.Background(), "node-a")
	if err != nil || len(info.Models) != 1 || info.Models[0].Observation == nil || info.Models[0].Observation.State != "present" {
		t.Fatal(info, err)
	}
}

func TestObservationRejectsStaleOrInvalidMeasurements(t *testing.T) {
	for _, stamp := range []time.Time{time.Time{}, time.Now().Add(-time.Minute), time.Now().Add(time.Minute)} {
		b := &SDKBackend{Client: &sdk.Client{}, Models: []Model{{ID: "one"}}, Observe: func(context.Context, []Model) ([]ModelObservation, *ResourceObservation, error) {
			return []ModelObservation{{State: "present", CheckedAt: stamp}}, nil, nil
		}}
		if _, err := b.Info(context.Background()); err == nil {
			t.Fatal("invalid time accepted", stamp)
		}
	}
	n := uint64(10)
	b := &SDKBackend{Client: &sdk.Client{}, Observe: func(context.Context, []Model) ([]ModelObservation, *ResourceObservation, error) {
		return nil, &ResourceObservation{State: "unknown", CheckedAt: time.Now().UTC(), AvailableRAM: &n}, nil
	}}
	if _, err := b.Info(context.Background()); err == nil {
		t.Fatal("unknown measurement with concrete capacity")
	}
}
