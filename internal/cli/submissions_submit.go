package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

// Extract only the intake-specific flag; all execution options are validated
// by the existing run parser. Flag values are never reinterpreted as options.
func parseSubmitArgs(args []string) (config.Options, app.Request, string, error) {
	var key string
	seen := false
	filtered := make([]string, 0, len(args))
	bad := func() (config.Options, app.Request, string, error) {
		return config.Options{}, app.Request{}, "", errors.New("invalid submission arguments")
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, equals := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "---") || arg == "--" {
			return bad()
		}
		if name == "json" {
			return bad()
		}
		if name == "key" {
			if seen {
				return bad()
			}
			seen = true
			if !equals {
				i++
				if i >= len(args) {
					return bad()
				}
				value = args[i]
			}
			key = value
			continue
		}
		filtered = append(filtered, arg)
		if !equals && name != "local-required" {
			i++
			if i >= len(args) {
				return bad()
			}
			filtered = append(filtered, args[i])
		}
	}
	if !seen || len(key) < 16 || len(key) > 128 || strings.ContainsFunc(key, func(c rune) bool { return c < 33 || c > 126 }) {
		return bad()
	}
	options, request, _, err := parseRunOptions(filtered)
	if err != nil {
		return bad()
	}
	return options, request, key, nil
}

func runSubmit(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	options, request, key, err := parseSubmitArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, "usage: nexus submit --config path --key idempotency-key --model id < prompt.txt")
		return 2
	}
	ctx, cancel := submissionCLIContext()
	defer cancel()
	options.Env = config.Environment(os.Environ())
	settings, err := config.Load(options)
	if err != nil {
		fmt.Fprintln(stderr, "cannot load submission configuration")
		return 1
	}
	prompt, err := readTaskPrompt(ctx, stdin)
	if err != nil {
		fmt.Fprintln(stderr, "cannot read UTF-8 prompt (maximum 1 MiB; submission deadline 30 seconds)")
		return 1
	}
	request.Prompt = prompt
	svc, err := app.NewService(settings, os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "invalid application configuration")
		return 1
	}
	status, err := svc.Submit(ctx, key, request)
	if err != nil {
		fmt.Fprintln(stderr, "submission intake failed; retry only with the same key and request")
		return 1
	}
	return writeSubmissionJSON(ctx, stdout, status)
}
