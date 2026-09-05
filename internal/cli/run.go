package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"darwinrouter/internal/app"
	"darwinrouter/internal/config"
	"darwinrouter/sessions"
)

// parseRunArgs validates constraints before configuration, storage or providers
// are opened and returns the request passed to the application service.
func parseRunArgs(args []string) (config.Options, app.Request, error) {
	var options config.Options
	var request app.Request
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&options.ProjectFile, "config", "", "project configuration")
	fs.StringVar(&options.UserFile, "user-config", "", "user configuration")
	fs.StringVar(&request.ModelID, "model", "", "configured model ID or auto")
	fs.StringVar(&request.ContinueTaskID, "continue-task", "", "completed task history to continue")
	compactKeep := fs.Int("compact-keep", 0, "recent messages to retain when compacting continued history")
	compactSummary := fs.String("compact-summary", "", "operator JSON summary file (maximum 64 KiB)")
	fs.StringVar(&request.Domain, "domain", "", "routing evidence domain")
	fs.StringVar(&request.Profile, "profile", "", "routing evidence profile")
	capabilityName := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	capabilities := map[string]bool{}
	fs.Func("capability", "required capability (repeatable)", func(value string) error {
		if !capabilityName.MatchString(value) || capabilities[value] {
			return fmt.Errorf("invalid capability")
		}
		capabilities[value] = true
		request.Capabilities = append(request.Capabilities, value)
		return nil
	})
	fs.IntVar(&request.ContextTokens, "context-tokens", 0, "minimum context tokens")
	fs.Float64Var(&request.MaxCost, "max-cost", 0, "maximum estimated route cost")
	fs.BoolVar(&request.LocalRequired, "local-required", false, "require a local model")
	fs.Func("validate", "output validation: go_source expects a raw full Go source file", func(value string) error {
		if value != "" && value != "go_source" {
			return fmt.Errorf("invalid output validation")
		}
		request.Validation = value
		return nil
	})
	values := overrides{}
	fs.Var(values, "set", "override scalar setting")
	if err := fs.Parse(args); err != nil {
		return options, request, err
	}
	if fs.NArg() != 0 || options.ProjectFile == "" || request.ModelID == "" || request.ContextTokens < 0 || request.MaxCost < 0 || math.IsNaN(request.MaxCost) || math.IsInf(request.MaxCost, 0) {
		return options, request, fmt.Errorf("invalid run arguments")
	}
	var invalidLabel bool
	var keepSet, summarySet bool
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "compact-keep" {
			keepSet = true
		}
		if f.Name == "compact-summary" {
			summarySet = true
		}
		if f.Name == "domain" || f.Name == "profile" {
			value := f.Value.String()
			if value == "" || len(value) > 128 || strings.TrimSpace(value) != value || strings.ContainsFunc(value, unicode.IsControl) {
				invalidLabel = true
			}
		}
	})
	if invalidLabel {
		return options, request, fmt.Errorf("invalid routing label")
	}
	if keepSet || summarySet {
		if !keepSet || !summarySet || *compactKeep < 1 || *compactSummary == "" || request.ContinueTaskID == "" {
			return options, request, fmt.Errorf("invalid compaction arguments")
		}
		summary, err := readCompactionSummary(*compactSummary)
		if err != nil {
			return options, request, err
		}
		request.Compaction = &sessions.CompactionRequest{Keep: *compactKeep, Summary: summary}
		if sessions.ValidateCompactionRequest(request.Compaction) != nil {
			return options, request, fmt.Errorf("invalid compaction summary")
		}
	}
	options.Flags = values
	return options, request, nil
}

func readCompactionSummary(path string) (sessions.Summary, error) {
	var summary sessions.Summary
	bad := fmt.Errorf("invalid compaction summary")
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return summary, bad
	}
	// Nonblocking open prevents a raced replacement with a FIFO from hanging.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return summary, bad
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return summary, bad
	}
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(data) > 64<<10 || !utf8.Valid(data) {
		return summary, bad
	}
	d := json.NewDecoder(bytes.NewReader(data))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return summary, bad
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return summary, bad
		}
		seen[key] = true
		var target *[]string
		switch key {
		case "requirements":
			target = &summary.Requirements
		case "activity":
			target = &summary.Activity
		case "decisions":
			target = &summary.Decisions
		case "pending_work":
			target = &summary.PendingWork
		case "failures":
			target = &summary.Failures
		case "artifacts":
			target = &summary.Artifacts
		default:
			return summary, bad
		}
		var value any
		if d.Decode(&value) != nil {
			return summary, bad
		}
		items, ok := value.([]any)
		if !ok {
			return summary, bad
		}
		*target = make([]string, len(items))
		for i, item := range items {
			text, ok := item.(string)
			if !ok {
				return summary, bad
			}
			(*target)[i] = text
		}
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') {
		return summary, bad
	}
	if _, err := d.Token(); err != io.EOF {
		return summary, bad
	}
	return summary, nil
}

func runTask(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	options, request, err := parseRunArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, "usage: darwin run --config path --model id|auto [--domain name] [--profile name] [--capability name ...] [--context-tokens n] [--max-cost n] [--local-required] [--validate go_source] < prompt.txt")
		fmt.Fprintln(stderr, "go_source validation expects output containing a raw full Go source file")
		fmt.Fprintln(stderr, "continuation compaction: --continue-task id --compact-keep n --compact-summary summary.json")
		return 2
	}
	options.Env = config.Environment(os.Environ())
	s, err := config.Load(options)
	if err != nil {
		fmt.Fprintln(stderr, "cannot load task configuration")
		return 1
	}
	prompt, err := io.ReadAll(io.LimitReader(stdin, (1<<20)+1))
	if err != nil || len(prompt) > 1<<20 {
		fmt.Fprintln(stderr, "cannot read prompt (maximum 1 MiB)")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	service, err := app.NewService(s, os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "invalid application configuration")
		return 1
	}
	request.Prompt = string(prompt)
	result, err := service.Run(ctx, request)
	if result.TaskID != "" {
		if _, writeErr := fmt.Fprintln(stderr, "Task:", result.TaskID); writeErr != nil {
			return 1
		}
	}
	if result.AuditStatus != "" {
		if _, writeErr := fmt.Fprintln(stderr, "Audit:", result.AuditStatus, result.AuditID); writeErr != nil {
			return 1
		}
	}
	for _, previous := range result.PreviousTaskIDs {
		if _, writeErr := fmt.Fprintln(stderr, "Previous attempt:", previous); writeErr != nil {
			return 1
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "task failed; inspect local task history before retrying")
		return 1
	}
	if _, err := fmt.Fprintln(stdout, result.Text); err != nil {
		return 1
	}
	return 0
}
