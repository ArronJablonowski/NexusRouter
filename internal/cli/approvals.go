package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func runApprovals(args []string, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: darwin approvals list --db path --task id [--after call-id --limit 25] | show --db path --task id --id approval-id")
		return 2
	}
	if len(args) < 1 || (args[0] != "list" && args[0] != "show") || len(args) > 11 {
		return usage()
	}
	flags := map[string]string{}
	for i := 1; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "--") {
			return usage()
		}
		key, value, equal := strings.Cut(strings.TrimPrefix(args[i], "--"), "=")
		if key != "db" && key != "task" && !(args[0] == "show" && key == "id") && !(args[0] == "list" && (key == "after" || key == "limit")) {
			return usage()
		}
		if _, exists := flags[key]; exists {
			return usage()
		}
		if !equal {
			i++
			if i >= len(args) {
				return usage()
			}
			value = args[i]
		}
		if value == "" {
			return usage()
		}
		flags[key] = value
	}
	if flags["db"] == "" || !sessions.ValidEventPageID(flags["task"]) {
		return usage()
	}
	options := approvals.ListOptions{TaskID: flags["task"], AfterCallID: flags["after"], Limit: 25}
	if value, ok := flags["limit"]; ok {
		for _, c := range value {
			if c < '0' || c > '9' {
				return usage()
			}
		}
		limit, err := strconv.Atoi(value)
		if err != nil {
			return usage()
		}
		options.Limit = limit
	}
	if options.Validate() != nil || (args[0] == "show" && !sessions.ValidEventPageID(flags["id"])) {
		return usage()
	}
	ctx, cancel := submissionCLIContext()
	defer cancel()
	var output any
	var err error
	if args[0] == "show" {
		output, err = app.InspectApproval(ctx, flags["db"], flags["task"], flags["id"])
	} else {
		output, err = app.ListApprovals(ctx, flags["db"], options)
	}
	if err != nil {
		fmt.Fprintln(stderr, "approval metadata unavailable")
		return 1
	}
	return writeSubmissionJSON(ctx, stdout, output)
}
