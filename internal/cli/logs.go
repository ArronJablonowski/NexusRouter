package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/diagnostics"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
)

const logsUsage = "usage: darwin logs --config path [--task id --after position --limit 100 --follow --include-content]"

func runLogs(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	values := map[string]string{}
	counts := map[string]int{}
	for _, name := range []string{"config", "task", "after", "limit"} {
		fs.Func(name, name, func(value string) error { counts[name]++; values[name] = value; return nil })
	}
	follow, includeContent := false, false
	fs.BoolFunc("follow", "follow committed activity until interrupted", func(value string) error {
		counts["follow"]++
		var err error
		follow, err = strconv.ParseBool(value)
		return err
	})
	fs.BoolFunc("include-content", "include private redacted session input/output", func(value string) error {
		counts["include-content"]++
		var err error
		includeContent, err = strconv.ParseBool(value)
		return err
	})
	valid := fs.Parse(args) == nil && fs.NArg() == 0 && counts["config"] == 1 && values["config"] != ""
	for name, count := range counts {
		valid = valid && count == 1 && (name == "follow" || name == "include-content" || values[name] != "")
	}
	options := diagnostics.Options{TaskID: values["task"], Limit: 100, IncludeContent: includeContent}
	if value := values["after"]; value != "" {
		var err error
		options.After, err = strconv.ParseInt(value, 10, 64)
		valid = valid && err == nil
	}
	if value := values["limit"]; value != "" {
		var err error
		options.Limit, err = strconv.Atoi(value)
		valid = valid && err == nil
	}
	if !valid || options.Validate() != nil {
		fmt.Fprintln(stderr, logsUsage)
		return 2
	}
	ctx, stop := submissionCLIContext()
	defer stop()
	return runLogsContext(ctx, values["config"], options, follow, follow && counts["after"] == 0 && options.TaskID == "", stdout, stderr)
}

func runLogsContext(ctx context.Context, path string, options diagnostics.Options, follow, startLatest bool, stdout, stderr io.Writer) int {
	fail := func() int { fmt.Fprintln(stderr, "diagnostic log unavailable"); return 1 }
	if ctx == nil || options.Validate() != nil {
		return fail()
	}
	canceled := func() bool { return follow && ctx.Err() != nil }
	if ctx.Err() != nil {
		if canceled() {
			return 0
		}
		return fail()
	}
	cfg, err := config.Load(config.Options{ProjectFile: path, Env: config.Environment(os.Environ())})
	if err != nil {
		return fail()
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		if canceled() {
			return 0
		}
		return fail()
	}
	defer db.Close()
	if startLatest {
		options.After, err = db.DiagnosticHead(ctx)
		if err != nil {
			if canceled() {
				return 0
			}
			return fail()
		}
	}
	secrets := logSecrets(cfg, os.Getenv)
	for {
		page, err := db.DiagnosticEvents(ctx, options)
		if err != nil {
			if follow && ctx.Err() != nil {
				return 0
			}
			return fail()
		}
		for _, record := range page.Records {
			body, encodeErr := diagnostics.EncodeLine(record, secrets)
			if encodeErr != nil || !writeLogLine(stdout, body) {
				return fail()
			}
		}
		// A checkpoint is a distinct JSONL type and is printed even for empty
		// pages so callers can resume after filtered heartbeat/delta events.
		checkpoint, _ := json.Marshal(struct {
			Version   int    `json:"version"`
			Kind      string `json:"kind"`
			NextAfter int64  `json:"next_after"`
			HasMore   bool   `json:"has_more"`
		}{1, "diagnostic.checkpoint", page.NextAfter, page.HasMore})
		if !writeLogLine(stdout, append(checkpoint, '\n')) {
			return fail()
		}
		if !follow {
			return 0
		}
		options.After = page.NextAfter
		if page.HasMore {
			continue
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0
		case <-timer.C:
		}
	}
}

func writeLogLine(writer io.Writer, body []byte) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	n, err := writer.Write(body)
	return err == nil && n == len(body)
}

func logSecrets(cfg config.Settings, secret func(string) string) []string {
	values := []string{secret("DARWIN_API_TOKEN")}
	for _, provider := range cfg.Providers {
		if provider.APIKeyEnv != "" {
			values = append(values, secret(provider.APIKeyEnv))
		}
	}
	for _, name := range cfg.Security.RedactEnv {
		values = append(values, secret(name))
	}
	if value := cfg.Telemetry.MetricsExport; value != nil && value.APIKeyEnv != "" {
		values = append(values, secret(value.APIKeyEnv))
	}
	if value := cfg.Telemetry.TraceExport; value != nil && value.APIKeyEnv != "" {
		values = append(values, secret(value.APIKeyEnv))
	}
	return values
}
