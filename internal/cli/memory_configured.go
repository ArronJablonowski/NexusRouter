package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/memory"
)

type configuredMemoryOptions struct {
	action, config, id, after, contains string
	expected                            int64
	limit                               int
	includeExpired                      bool
}

// Called only after FlagSet.Parse succeeds. Preserve flag-looking string
// values and bool flags while rejecting overwritten operator selections.
func configuredMemoryUniqueFlags(fs *flag.FlagSet, args []string) bool {
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			return i == len(args)-1
		}
		name, _, assigned := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		f := fs.Lookup(name)
		if f == nil || seen[name] {
			return false
		}
		seen[name] = true
		boolean, ok := f.Value.(interface{ IsBoolFlag() bool })
		if !assigned && (!ok || !boolean.IsBoolFlag()) {
			i++
		}
	}
	return true
}

func (o configuredMemoryOptions) validFlags(visited map[string]bool) bool {
	if o.config == "" || visited["db"] || visited["scope"] {
		return false
	}
	allowed := map[string]bool{"config": true}
	switch o.action {
	case "export":
		// Export is the complete configured scope, not a filtered live page.
	case "list":
		for _, name := range []string{"limit", "after", "contains", "include-expired"} {
			allowed[name] = true
		}
		if o.limit < 1 || o.limit > 100 || (memory.Query{Scope: "validation", AfterID: o.after, Contains: o.contains, Limit: o.limit, Now: time.Now().UTC()}).Validate() != nil {
			return false
		}
	case "show":
		allowed["id"] = true
		if !memory.ValidKey(o.id) {
			return false
		}
	case "put":
		allowed["id"], allowed["expected"] = true, true
		if !visited["expected"] || o.expected < 0 || (visited["id"] && !memory.ValidKey(o.id)) {
			return false
		}
	case "delete":
		allowed["id"], allowed["expected"] = true, true
		if !memory.ValidKey(o.id) || !visited["expected"] || o.expected < 1 {
			return false
		}
	default:
		return false
	}
	for name := range visited {
		if !allowed[name] {
			return false
		}
	}
	return true
}

// Configured management shares the API/SDK scope and credential boundary.
// It never initializes storage, starts inference, or retries a mutation.
func runConfiguredMemory(o configuredMemoryOptions, stdin io.Reader, stdout, stderr io.Writer) int {
	fail := func() int {
		fmt.Fprintln(stderr, "memory operation failed; check configuration, scope, identity and revision")
		return 1
	}
	var fact memory.Fact
	if o.action == "put" {
		var err error
		fact, err = decodeConfiguredMemoryFact(stdin)
		if err != nil || fact.Revision-1 != o.expected || (o.id != "" && o.id != fact.ID) {
			return fail()
		}
	}
	cfg, err := config.Load(config.Options{ProjectFile: o.config, Env: config.Environment(os.Environ())})
	if err != nil {
		return fail()
	}
	service, err := app.NewService(cfg, os.Getenv)
	if err != nil {
		return fail()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var output any
	switch o.action {
	case "export":
		output, err = service.ExportMemory(ctx)
	case "list":
		output, err = service.Memories(ctx, o.after, o.contains, o.limit, o.includeExpired)
	case "show":
		output, err = service.Memory(ctx, o.id)
	case "put":
		err = service.PutMemory(ctx, fact, o.expected)
		output = map[string]any{"id": fact.ID, "revision": fact.Revision}
	case "delete":
		err = service.DeleteMemory(ctx, o.id, o.expected)
		output = map[string]any{"id": o.id, "deleted": true}
	}
	if err != nil {
		return fail()
	}
	if o.action == "export" {
		return writeMemoryExport(stdout, output, fail)
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if encoder.Encode(output) != nil {
		return 1
	}
	return 0
}

// The complete compact envelope is buffered before publishing any bytes. A
// failed stdout write can still leave a partial external copy; never retry it.
func writeMemoryExport(stdout io.Writer, output any, fail func() int) int {
	body, err := json.Marshal(output)
	if err != nil || len(body) > memory.ExportMaxBytes {
		return fail()
	}
	body = append(body, '\n')
	n, err := stdout.Write(body)
	if err != nil || n != len(body) {
		return 1
	}
	return 0
}
