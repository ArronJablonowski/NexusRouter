package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func runScopeLeases(args []string, stdout, stderr io.Writer) int {
	ctx, stop := submissionCLIContext()
	defer stop()
	return runScopeLeasesContext(ctx, args, stdout, stderr)
}

func runScopeLeasesContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags, ok := scopeLeaseFlags(args)
	if !ok {
		fmt.Fprintln(stderr, "usage: darwin resources leases --db path --scope scope")
		return 2
	}
	status, err := app.InspectScopeLeases(ctx, flags["db"], flags["scope"])
	if err != nil {
		fmt.Fprintln(stderr, "scope lease status missing, incomplete or invalid")
		return 1
	}
	return writeSubmissionJSON(ctx, stdout, status)
}

func scopeLeaseFlags(args []string) (map[string]string, bool) {
	values := map[string]string{}
	for i := 0; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "--") {
			return nil, false
		}
		key, value, equals := strings.Cut(strings.TrimPrefix(args[i], "--"), "=")
		if key != "db" && key != "scope" {
			return nil, false
		}
		if _, exists := values[key]; exists {
			return nil, false
		}
		if !equals {
			i++
			if i >= len(args) || strings.HasPrefix(args[i], "--") {
				return nil, false
			}
			value = args[i]
		}
		if value == "" {
			return nil, false
		}
		values[key] = value
	}
	return values, len(values) == 2 && workers.ValidLeaseScope(values["scope"])
}
