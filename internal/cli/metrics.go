package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"darwinrouter/internal/telemetry"
)

func runMetrics(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("metrics", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := ""
	count := 0
	fs.Func("db", "existing database", func(value string) error { count++; path = value; return nil })
	if fs.Parse(args) != nil || fs.NArg() != 0 || count != 1 || path == "" {
		fmt.Fprintln(stderr, "usage: darwin metrics --db path")
		return 2
	}
	ctx, stop := submissionCLIContext()
	defer stop()
	return runMetricsContext(ctx, path, stdout, stderr)
}

func runMetricsContext(parent context.Context, path string, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	fail := func() int { fmt.Fprintln(stderr, "metrics snapshot unavailable"); return 1 }
	if ctx.Err() != nil {
		return fail()
	}
	db, err := telemetry.OpenReadOnly(ctx, path)
	if err != nil {
		return fail()
	}
	defer db.Close()
	snapshot, err := db.Metrics(ctx)
	if err != nil || snapshot.Validate() != nil || ctx.Err() != nil {
		return fail()
	}
	return writeSubmissionJSON(ctx, stdout, snapshot)
}
