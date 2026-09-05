package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"darwinrouter/internal/app"
	"darwinrouter/internal/config"
	"darwinrouter/internal/telemetry"
	"darwinrouter/sessions"
	"darwinrouter/submissions"
)

func submissionCLIContext() (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	return ctx, func() { cancel(); stop() }
}

func runSubmissions(args []string, stdout, stderr io.Writer) int {
	invalid := func() int {
		fmt.Fprintln(stderr, "usage: darwin submissions list|show|cancel --db path [--id id] [--state state --after cursor --limit 25]")
		return 2
	}
	if len(args) == 0 {
		return invalid()
	}
	verb := args[0]
	if verb != "list" && verb != "show" && verb != "cancel" {
		return invalid()
	}
	seen := map[string]bool{}
	for i := 1; i < len(args); i++ {
		name, _, equals := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		if !strings.HasPrefix(args[i], "-") || seen[name] {
			return invalid()
		}
		seen[name] = true
		if !equals {
			i++
		}
	}
	fs := flag.NewFlagSet("submissions", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	database := fs.String("db", "", "database")
	id := fs.String("id", "", "submission ID")
	state := fs.String("state", "", "state")
	after := fs.String("after", "", "opaque cursor")
	limit := fs.Int("limit", 25, "page limit")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *database == "" {
		return invalid()
	}
	bad := false
	fs.Visit(func(f *flag.Flag) {
		if verb == "list" && f.Name == "id" || verb != "list" && (f.Name == "state" || f.Name == "after" || f.Name == "limit") {
			bad = true
		}
	})
	if bad || verb != "list" && *id == "" || *limit < 1 || *limit > 100 {
		return invalid()
	}
	if verb != "list" && !sessions.ValidEventPageID(*id) {
		return invalid()
	}
	if *state != "" && *state != "queued" && *state != "running" && *state != "succeeded" && *state != "failed" && *state != "canceled" {
		return invalid()
	}
	listOptions := submissions.ListOptions{State: *state, After: *after, Limit: *limit}
	if verb == "list" && listOptions.Validate() != nil {
		return invalid()
	}
	ctx, cancel := submissionCLIContext()
	defer cancel()
	var output any
	var err error
	if verb == "cancel" {
		cfg := config.Defaults()
		cfg.Telemetry.Database = *database
		svc, createErr := app.NewService(cfg, os.Getenv)
		if createErr != nil {
			err = createErr
		} else {
			output, err = svc.CancelSubmission(ctx, *id)
		}
	} else {
		db, openErr := telemetry.OpenReadOnly(ctx, *database)
		if openErr != nil {
			err = openErr
		} else {
			defer db.Close()
			if verb == "show" {
				output, err = db.Submission(ctx, *id)
			} else {
				output, err = db.ListSubmissions(ctx, listOptions)
			}
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "submission operation failed")
		return 1
	}
	return writeSubmissionJSON(ctx, stdout, output)
}

func writeSubmissionJSON(ctx context.Context, output io.Writer, value any) (exitCode int) {
	pipeSignals := make(chan os.Signal, 1)
	signal.Notify(pipeSignals, syscall.SIGPIPE)
	defer signal.Stop(pipeSignals)
	defer func() {
		if recover() != nil {
			exitCode = 1
		}
	}()
	writer, cleanup, err := prepareStreamOutput(ctx, output)
	if err != nil {
		return 1
	}
	defer func() {
		if cleanup() != nil {
			exitCode = 1
		}
	}()
	err = json.NewEncoder(writer).Encode(value)
	if err != nil {
		return 1
	}
	return 0
}
