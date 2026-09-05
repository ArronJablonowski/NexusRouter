package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"darwinrouter/resources"
)

func TestResourcesInventoryPreservesHostFields(t *testing.T) {
	profile := func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{CPUs: 4, TotalRAM: 100, AvailableRAM: 50}, nil
	}
	survey := func(context.Context) (resources.GPUInventory, error) {
		return resources.GPUInventory{Sources: []resources.GPUObservation{{Source: "nvidia-smi", Status: "unavailable", Devices: []resources.GPUDevice{}}}}, nil
	}
	var output, stderr bytes.Buffer
	if code := runResourcesContext(context.Background(), &output, &stderr, profile, survey); code != 0 {
		t.Fatalf("%d %s", code, stderr.String())
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(output.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["TotalRAM"]) != "100" || string(fields["cpu_threads"]) != "4" || len(fields["gpu_inventory"]) == 0 || string(fields["VRAMTotal"]) != "null" {
		t.Fatal(output.String())
	}
}

func TestResourcesInventoryFailuresAreGeneric(t *testing.T) {
	profile := func(context.Context) (resources.Snapshot, error) { return resources.Snapshot{}, nil }
	survey := func(context.Context) (resources.GPUInventory, error) {
		return resources.GPUInventory{}, errors.New("private-driver-error")
	}
	var out, stderr bytes.Buffer
	if code := runResourcesContext(context.Background(), &out, &stderr, profile, survey); code != 1 || out.Len() != 0 || bytes.Contains(stderr.Bytes(), []byte("private")) {
		t.Fatalf("%d %s %s", code, &out, &stderr)
	}
	survey = func(context.Context) (resources.GPUInventory, error) { return resources.GPUInventory{}, nil }
	if code := runResourcesContext(context.Background(), failingResourceWriter{}, io.Discard, profile, survey); code != 1 {
		t.Fatal(code)
	}
}

type failingResourceWriter struct{}

func (failingResourceWriter) Write([]byte) (int, error) { return 0, errors.New("broken output") }
