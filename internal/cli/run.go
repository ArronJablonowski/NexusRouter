package cli

import (
	"context"
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

	"darwinrouter/internal/app"
	"darwinrouter/internal/config"
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
	values := overrides{}
	fs.Var(values, "set", "override scalar setting")
	if err := fs.Parse(args); err != nil {
		return options, request, err
	}
	if fs.NArg() != 0 || options.ProjectFile == "" || request.ModelID == "" || request.ContextTokens < 0 || request.MaxCost < 0 || math.IsNaN(request.MaxCost) || math.IsInf(request.MaxCost, 0) {
		return options, request, fmt.Errorf("invalid run arguments")
	}
	var invalidLabel bool
	fs.Visit(func(f *flag.Flag) {
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
	options.Flags = values
	return options, request, nil
}

func runTask(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	options, request, err := parseRunArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, "usage: darwin run --config path --model id|auto [--domain name] [--profile name] [--capability name ...] [--context-tokens n] [--max-cost n] [--local-required] < prompt.txt")
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
