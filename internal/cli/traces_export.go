package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/traces"
)

func runTraces(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "export" {
		fmt.Fprintln(stderr, "usage: nexus traces export --config path --endpoint URL [--api-key-env ENV_NAME] [--limit 16]")
		return 2
	}
	fs := flag.NewFlagSet("traces export", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	values := map[string]string{}
	counts := map[string]int{}
	for _, name := range []string{"config", "endpoint", "api-key-env", "limit"} {
		fs.Func(name, name, func(value string) error {
			counts[name]++
			values[name] = value
			return nil
		})
	}
	valid := fs.Parse(args[1:]) == nil && fs.NArg() == 0 && counts["config"] == 1 && counts["endpoint"] == 1
	for name, count := range counts {
		valid = valid && count == 1 && values[name] != ""
	}
	limit := 0
	if values["limit"] != "" {
		var err error
		limit, err = strconv.Atoi(values["limit"])
		valid = valid && err == nil
	}
	options := traces.ExportOptions{Endpoint: values["endpoint"], APIKeyEnv: values["api-key-env"], Limit: limit}
	if !valid || options.Validate() != nil {
		fmt.Fprintln(stderr, "usage: nexus traces export --config path --endpoint URL [--api-key-env ENV_NAME] [--limit 16]")
		return 2
	}
	ctx, stop := submissionCLIContext()
	defer stop()
	return runTracesExportContext(ctx, values["config"], options, stdout, stderr)
}

func runTracesExportContext(parent context.Context, path string, options traces.ExportOptions, stdout, stderr io.Writer) int {
	fail := func() int { fmt.Fprintln(stderr, "trace export unavailable"); return 1 }
	if parent == nil || parent.Err() != nil {
		return fail()
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	cfg, err := config.Load(config.Options{ProjectFile: path, Env: config.Environment(os.Environ())})
	if err != nil {
		return fail()
	}
	service, err := app.NewService(cfg, os.Getenv)
	if err != nil || service.ExportTraces(ctx, options) != nil {
		return fail()
	}
	return writeSubmissionJSON(ctx, stdout, struct {
		Exported bool `json:"exported"`
	}{Exported: true})
}
