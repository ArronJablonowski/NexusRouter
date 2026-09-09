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

func runSessionInspection(args []string, stdout, stderr io.Writer) int {
	invalid := func() int {
		fmt.Fprintln(stderr, "usage: darwin session tasks --db path --session id [--after cursor --limit 25]")
		return 2
	}
	if len(args) == 0 || args[0] != "tasks" {
		return invalid()
	}
	args = args[1:]
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
	fs := flag.NewFlagSet("session tasks", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	database := fs.String("db", "", "existing database")
	session := fs.String("session", "", "session ID")
	after := fs.String("after", "", "opaque cursor")
	limit := fs.Int("limit", 25, "page limit")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *database == "" {
		return invalid()
	}
	options := sessions.SessionTaskListOptions{After: *after, Limit: *limit}
	if options.Validate(*session) != nil {
		return invalid()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := telemetry.OpenReadOnly(ctx, *database)
	if err != nil {
		fmt.Fprintln(stderr, "session tasks unavailable or invalid")
		return 1
	}
	defer db.Close()
	page, err := db.ListSessionTasks(ctx, *session, options)
	if err != nil || page.Validate() != nil {
		fmt.Fprintln(stderr, "session tasks unavailable or invalid")
		return 1
	}
	body, err := json.Marshal(page)
	if err != nil || len(body) > sessions.MaxSessionTaskPageBytes {
		return 1
	}
	body = append(body, '\n')
	n, err := stdout.Write(body)
	if err != nil || n != len(body) {
		return 1
	}
	return 0
}
