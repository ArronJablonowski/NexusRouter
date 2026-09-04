package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"darwinrouter/resources"
)

func runResources(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: darwin resources")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snapshot, err := resources.Profile(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "resource measurements unavailable")
		return 1
	}
	if json.NewEncoder(stdout).Encode(snapshot) != nil {
		return 1
	}
	return 0
}
