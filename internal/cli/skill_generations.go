package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
)

func runSkillGenerations(args []string, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: darwin skill-generations list|show --db path --scope id [--id id] [--after id --limit 25]")
		return 2
	}
	if len(args) == 0 || (args[0] != "list" && args[0] != "show") {
		return usage()
	}
	command := args[0]
	flags := map[string]string{}
	for i := 1; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "--") {
			return usage()
		}
		key, value, equals := strings.Cut(strings.TrimPrefix(args[i], "--"), "=")
		if key != "db" && key != "scope" && !(command == "show" && key == "id") && !(command == "list" && (key == "after" || key == "limit")) {
			return usage()
		}
		if _, exists := flags[key]; exists {
			return usage()
		}
		if !equals {
			i++
			if i >= len(args) || strings.HasPrefix(args[i], "--") {
				return usage()
			}
			value = args[i]
		}
		if value == "" {
			return usage()
		}
		flags[key] = value
	}
	if flags["db"] == "" || !skillIdentifier.MatchString(flags["scope"]) || (command == "show" && !skillIdentifier.MatchString(flags["id"])) || (flags["after"] != "" && !skillIdentifier.MatchString(flags["after"])) {
		return usage()
	}
	limit := 25
	if text, exists := flags["limit"]; exists {
		parsed, err := strconv.Atoi(text)
		if err != nil || parsed < 1 || parsed > 100 || strconv.Itoa(parsed) != text {
			return usage()
		}
		limit = parsed
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var output any
	var err error
	if command == "show" {
		output, err = app.InspectSkillGeneration(ctx, flags["db"], flags["scope"], flags["id"])
	} else {
		output, err = app.ListSkillGenerations(ctx, flags["db"], flags["scope"], flags["after"], limit)
	}
	if err != nil {
		fmt.Fprintln(stderr, "skill generation inspection unavailable")
		return 1
	}
	return writeSubmissionJSON(ctx, stdout, output)
}
