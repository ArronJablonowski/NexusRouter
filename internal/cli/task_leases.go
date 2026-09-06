package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
)

func runTaskLeases(args []string, stdout, stderr io.Writer) int {
	flags, err := parseSteeringFlags(args, "db", "task")
	if err != nil {
		fmt.Fprintln(stderr, "usage: darwin task leases --db path --task id")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status, err := app.InspectTaskLeases(ctx, flags["db"], flags["task"])
	if err != nil {
		fmt.Fprintln(stderr, "task lease status missing, incomplete or invalid")
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if encoder.Encode(status) != nil {
		return 1
	}
	return 0
}
