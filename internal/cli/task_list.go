package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func runTaskList(args []string, stdout, stderr io.Writer) int {
	invalid := func() int {
		fmt.Fprintln(stderr, "usage: darwin task list --db path [--state state --after cursor --limit 25]")
		return 2
	}
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		name, _, equals := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		if !strings.HasPrefix(args[i], "-") || seen[name] {
			return invalid()
		}
		seen[name] = true
		if !equals {
			i++
		}
	}
	fs := flag.NewFlagSet("task list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	database := fs.String("db", "", "existing database")
	state := fs.String("state", "", "task state")
	after := fs.String("after", "", "opaque cursor")
	limit := fs.Int("limit", 25, "page limit")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *database == "" {
		return invalid()
	}
	options := sessions.TaskListOptions{State: *state, After: *after, Limit: *limit}
	if options.Validate() != nil {
		return invalid()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := telemetry.OpenReadOnly(ctx, *database)
	if err != nil {
		fmt.Fprintln(stderr, "task list unavailable or invalid")
		return 1
	}
	defer db.Close()
	page, err := db.ListTasks(ctx, options)
	if err != nil {
		fmt.Fprintln(stderr, "task list unavailable or invalid")
		return 1
	}
	body, err := json.Marshal(page)
	if err != nil || len(body) > sessions.MaxTaskPageBytes {
		return 1
	}
	body = append(body, '\n')
	n, err := stdout.Write(body)
	if err != nil || n != len(body) {
		return 1
	}
	return 0
}
