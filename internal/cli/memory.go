package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/memory"
)

func runMemory(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: darwin memory list|show|put|delete (--config path | --db path --scope scope) [--id id] [--expected revision]")
		return 2
	}
	if len(args) == 0 {
		return usage()
	}
	action := args[0]
	if action != "list" && action != "show" && action != "put" && action != "delete" {
		return usage()
	}
	fs := flag.NewFlagSet("memory", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", "", "configured scoped memory management")
	path := fs.String("db", "", "memory database")
	scope := fs.String("scope", "", "exact scope")
	id := fs.String("id", "", "fact ID")
	expected := fs.Int64("expected", 0, "expected revision")
	limit := fs.Int("limit", 100, "page size")
	after := fs.String("after", "", "exclusive ID cursor")
	contains := fs.String("contains", "", "literal content filter")
	expired := fs.Bool("include-expired", false, "include expired facts")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		return usage()
	}
	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	if visited["config"] {
		options := configuredMemoryOptions{action: action, config: *configPath, id: *id, expected: *expected, limit: *limit, after: *after, contains: *contains, includeExpired: *expired}
		if !options.validFlags(visited) || !configuredMemoryUniqueFlags(fs, args[1:]) {
			return usage()
		}
		return runConfiguredMemory(options, stdin, stdout, stderr)
	}
	if *path == "" || !memory.ValidKey(*scope) || ((action == "show" || action == "delete") && !memory.ValidKey(*id)) || (action == "delete" && *expected < 1) {
		return usage()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var fact memory.Fact
	if action == "put" {
		b, err := io.ReadAll(io.LimitReader(stdin, (1<<20)+1))
		if err != nil || len(b) > 1<<20 {
			fmt.Fprintln(stderr, "invalid memory record")
			return 1
		}
		if json.Unmarshal(b, &fact) != nil || fact.Validate() != nil || fact.Scope != *scope || (*id != "" && fact.ID != *id) || *expected < 0 || fact.Revision-1 != *expected {
			fmt.Fprintln(stderr, "invalid memory record or expected revision")
			return 1
		}
	}
	var db *telemetry.Store
	var err error
	if action == "delete" {
		info, statErr := os.Stat(*path)
		if statErr != nil || !info.Mode().IsRegular() {
			fmt.Fprintln(stderr, "cannot open existing memory database")
			return 1
		}
	}
	if action == "list" || action == "show" {
		db, err = telemetry.OpenReadOnly(ctx, *path)
	} else {
		db, err = telemetry.Open(ctx, *path)
	}
	if err != nil {
		fmt.Fprintln(stderr, "cannot open memory database")
		return 1
	}
	defer db.Close()
	var output any
	switch action {
	case "list":
		output, err = db.QueryMemory(ctx, memory.Query{Scope: *scope, AfterID: *after, Contains: *contains, Limit: *limit, Now: time.Now().UTC(), LocalOnly: true, IncludeExpired: *expired})
	case "show":
		output, err = db.GetMemory(ctx, *scope, *id)
	case "put":
		err = db.PutMemory(ctx, fact, *expected)
		output = map[string]any{"id": fact.ID, "revision": fact.Revision}
	case "delete":
		err = db.DeleteMemory(ctx, *scope, *id, *expected)
		output = map[string]any{"id": *id, "deleted": true}
	}
	if err != nil {
		fmt.Fprintln(stderr, "memory operation failed; check scope, identity and revision")
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if encoder.Encode(output) != nil {
		return 1
	}
	return 0
}
