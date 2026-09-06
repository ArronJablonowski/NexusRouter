package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func runLeaseAttentionHistory(args []string, stdout, stderr io.Writer) int {
	ctx, stop := submissionCLIContext()
	defer stop()
	return runLeaseAttentionHistoryContext(ctx, args, stdout, stderr)
}

func runLeaseAttentionHistoryContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	path, id, options, ok := leaseAttentionHistoryFlags(args)
	if !ok {
		fmt.Fprintln(stderr, "usage: darwin resources attention-history --db path --id ID [--after-sequence 0] [--limit 25]")
		return 2
	}
	page, err := app.InspectLeaseAttentionHistory(ctx, path, id, options)
	if err != nil {
		fmt.Fprintln(stderr, "lease attention history unavailable or invalid")
		return 1
	}
	return writeSubmissionJSON(ctx, stdout, page)
}

func leaseAttentionHistoryFlags(args []string) (string, string, workers.LeaseAttentionHistoryOptions, bool) {
	options := workers.LeaseAttentionHistoryOptions{Limit: 25}
	bad := func() (string, string, workers.LeaseAttentionHistoryOptions, bool) {
		return "", "", workers.LeaseAttentionHistoryOptions{}, false
	}
	values := map[string]string{}
	for i := 0; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "--") {
			return bad()
		}
		key, value, equals := strings.Cut(strings.TrimPrefix(args[i], "--"), "=")
		if key != "db" && key != "id" && key != "after-sequence" && key != "limit" {
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
	if values["db"] == "" || !sessions.ValidEventPageID(values["id"]) {
		return bad()
	}
	if raw, ok := values["after-sequence"]; ok {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || strconv.FormatInt(value, 10) != raw {
			return bad()
		}
		options.AfterSequence = value
	}
	if raw, ok := values["limit"]; ok {
		value, err := strconv.Atoi(raw)
		if err != nil || strconv.Itoa(value) != raw {
			return bad()
		}
		options.Limit = value
	}
	if options.Validate() != nil {
		return bad()
	}
	return values["db"], values["id"], options, true
}
