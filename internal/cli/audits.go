package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"darwinrouter/internal/telemetry"
)

func runAudits(args []string, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: darwin audits list|show|attempts --db path [--task id] [--id audit-id] [--after id] [--limit n]")
		return 2
	}
	if len(args) == 0 || (args[0] != "list" && args[0] != "show" && args[0] != "attempts") {
		return usage()
	}
	fs := flag.NewFlagSet("audits", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("db", "", "existing database")
	task := fs.String("task", "", "task ID")
	id := fs.String("id", "", "audit ID")
	after := fs.String("after", "", "exclusive audit ID cursor")
	limit := fs.Int("limit", 100, "page size")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *path == "" || (args[0] == "show" && (*id == "" || *task != "" || *after != "")) || (args[0] != "show" && (*task == "" || *id != "")) || *limit < 1 || *limit > 100 {
		return usage()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := telemetry.OpenReadOnly(ctx, *path)
	if err != nil {
		fmt.Fprintln(stderr, "audit database unavailable")
		return 1
	}
	defer db.Close()
	var out any
	if args[0] == "show" {
		out, err = db.Audit(ctx, *id)
	} else if args[0] == "attempts" {
		out, err = db.ReviewAttempts(ctx, *task, *after, *limit)
	} else {
		out, err = db.Audits(ctx, *task, *after, *limit)
	}
	if err != nil {
		fmt.Fprintln(stderr, "audit records unavailable")
		return 1
	}
	if json.NewEncoder(stdout).Encode(out) != nil {
		return 1
	}
	return 0
}
