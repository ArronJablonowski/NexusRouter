package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func runWorkflowDiscovery(args []string, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: darwin skill-generations discover --config path --domain id [--after cursor --scan-limit 20]")
		return 2
	}
	if len(args) == 0 || args[0] != "discover" {
		return usage()
	}
	flags := map[string]string{}
	for i := 1; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "--") {
			return usage()
		}
		key, value, equals := strings.Cut(strings.TrimPrefix(args[i], "--"), "=")
		if key != "config" && key != "domain" && key != "after" && key != "scan-limit" {
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
		if strings.TrimSpace(value) == "" {
			return usage()
		}
		flags[key] = value
	}
	if flags["config"] == "" || strings.ContainsRune(flags["config"], 0) || !skillIdentifier.MatchString(flags["domain"]) || (flags["after"] != "" && !sessions.ValidEventPageID(flags["after"])) {
		return usage()
	}
	limit := 20
	if value, ok := flags["scan-limit"]; ok {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 20 || strconv.Itoa(n) != value {
			return usage()
		}
		limit = n
	}
	cfg, err := config.Load(config.Options{ProjectFile: flags["config"], Env: config.Environment(os.Environ())})
	if err != nil {
		fmt.Fprintln(stderr, "workflow discovery configuration unavailable")
		return 1
	}
	svc, err := app.NewService(cfg, os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "workflow discovery configuration invalid")
		return 1
	}
	ctx, stop := submissionCLIContext()
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	page, err := svc.DiscoverSkillWorkflows(ctx, flags["domain"], flags["after"], limit)
	if err != nil {
		fmt.Fprintln(stderr, "workflow discovery unavailable")
		return 1
	}
	return writeSubmissionJSON(ctx, stdout, page)
}
