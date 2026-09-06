package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/resources"
)

func runResources(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "attention" {
		return runLeaseAttention(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "leases" {
		return runScopeLeases(args[1:], stdout, stderr)
	}
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: darwin resources")
		return 2
	}
	parent, stop := submissionCLIContext()
	defer stop()
	return runResourcesContext(parent, stdout, stderr, resources.Profile, resources.SurveyGPUs)
}

func runResourcesContext(parent context.Context, stdout, stderr io.Writer, profile func(context.Context) (resources.Snapshot, error), survey func(context.Context) (resources.GPUInventory, error)) int {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	snapshot, err := profile(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "resource measurements unavailable")
		return 1
	}
	gpus, err := survey(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "resource measurements unavailable")
		return 1
	}
	// Embed the existing snapshot to preserve its JSON fields. GPU observations
	// are diagnostic only until model/device placement can be verified.
	report := struct {
		resources.Snapshot
		GPUs resources.GPUInventory `json:"gpu_inventory"`
	}{snapshot, gpus}
	return writeSubmissionJSON(ctx, stdout, report)
}
