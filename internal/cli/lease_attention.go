package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/workers"
)

func runLeaseAttention(args []string, stdout, stderr io.Writer) int {
	ctx, stop := submissionCLIContext()
	defer stop()
	return runLeaseAttentionContext(ctx, args, stdout, stderr)
}

func runLeaseAttentionContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	path, options, ok := leaseAttentionFlags(args)
	if !ok {
		fmt.Fprintln(stderr, "usage: nexus resources attention --db path [--state open|resolved|all] [--after cursor] [--limit 25]")
		return 2
	}
	page, err := app.InspectLeaseAttention(ctx, path, options)
	if err != nil {
		fmt.Fprintln(stderr, "lease attention unavailable or invalid")
		return 1
	}
	return writeSubmissionJSON(ctx, stdout, page)
}

func leaseAttentionFlags(args []string) (string, workers.LeaseAttentionOptions, bool) {
	options := workers.LeaseAttentionOptions{State: "open", Limit: 25}
	bad := func() (string, workers.LeaseAttentionOptions, bool) {
		return "", workers.LeaseAttentionOptions{}, false
	}
	values := map[string]string{}
	for i := 0; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "--") {
			return bad()
		}
		key, value, equals := strings.Cut(strings.TrimPrefix(args[i], "--"), "=")
		if key != "db" && key != "state" && key != "after" && key != "limit" {
			return bad()
		}
		if _, exists := values[key]; exists {
			return bad()
		}
		if !equals {
			i++
			if i >= len(args) || strings.HasPrefix(args[i], "--") {
				return bad()
			}
			value = args[i]
		}
		if value == "" {
			return bad()
		}
		values[key] = value
	}
	if values["db"] == "" {
		return bad()
	}
	if state, ok := values["state"]; ok {
		options.State = state
	}
	options.After = values["after"]
	if limit, ok := values["limit"]; ok {
		parsed, err := strconv.Atoi(limit)
		if err != nil || strconv.Itoa(parsed) != limit {
			return bad()
		}
		options.Limit = parsed
	}
	if options.Validate() != nil {
		return bad()
	}
	return values["db"], options, true
}
