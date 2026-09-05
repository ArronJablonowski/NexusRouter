package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func runTaskInspection(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "show" {
		fmt.Fprintln(stderr, "usage: darwin task show --db path --task id")
		return 2
	}
	fs := flag.NewFlagSet("task show", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("db", "", "existing database")
	task := fs.String("task", "", "task ID")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *path == "" || *task == "" {
		fmt.Fprintln(stderr, "usage: darwin task show --db path --task id")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := telemetry.OpenReadOnly(ctx, *path)
	if err != nil {
		fmt.Fprintln(stderr, "cannot open existing task database")
		return 1
	}
	defer db.Close()
	snapshot, err := sessions.Replay(ctx, db, *task)
	if err != nil {
		fmt.Fprintln(stderr, "task history missing, incomplete or invalid")
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if encoder.Encode(snapshot) != nil {
		return 1
	}
	return 0
}
