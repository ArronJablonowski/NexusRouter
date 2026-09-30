package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/metrics"
)

// Export is an explicit one-shot operation, independent of daemon scheduling.
func runMetricsExport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("metrics export", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	values := map[string]string{}
	counts := map[string]int{}
	for _, name := range []string{"config", "endpoint", "api-key-env"} {
		fs.Func(name, name, func(value string) error {
			counts[name]++
			values[name] = value
			return nil
		})
	}
	valid := fs.Parse(args) == nil && fs.NArg() == 0 && counts["config"] == 1 && counts["endpoint"] == 1
	for name, count := range counts {
		valid = valid && count == 1 && values[name] != ""
	}
	options := metrics.ExportOptions{Endpoint: values["endpoint"], APIKeyEnv: values["api-key-env"]}
	if !valid || options.Validate() != nil {
		fmt.Fprintln(stderr, "usage: nexus metrics export --config path --endpoint URL [--api-key-env ENV_NAME]")
		return 2
	}
	ctx, stop := submissionCLIContext()
	defer stop()
	return runMetricsExportContext(ctx, values["config"], options, stdout, stderr)
}

func runMetricsExportContext(parent context.Context, path string, options metrics.ExportOptions, stdout, stderr io.Writer) int {
	fail := func() int { fmt.Fprintln(stderr, "metrics export unavailable"); return 1 }
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
	if err != nil || service.ExportMetrics(ctx, options) != nil {
		return fail()
	}
	return writeSubmissionJSON(ctx, stdout, struct {
		Exported bool `json:"exported"`
	}{Exported: true})
}
