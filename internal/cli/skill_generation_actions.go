package cli

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

var generationModelIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// runSkillGenerationAction requires explicit configuration and a caller-owned
// stable attempt ID. Neither action activates a skill or retries inference.
func runSkillGenerationAction(args []string, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: nexus skill-generations generate --config path --id id --model id --name name --tasks id,id [--max-cost amount] | publish --config path --id id")
		return 2
	}
	if len(args) == 0 || (args[0] != "generate" && args[0] != "publish") {
		return usage()
	}
	generate := args[0] == "generate"
	flags := map[string]string{}
	for i := 1; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "--") {
			return usage()
		}
		key, value, equals := strings.Cut(strings.TrimPrefix(args[i], "--"), "=")
		if key != "config" && key != "id" && !(generate && (key == "model" || key == "name" || key == "tasks" || key == "max-cost")) {
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
		if value == "" || strings.TrimSpace(value) == "" {
			return usage()
		}
		flags[key] = value
	}
	if flags["config"] == "" || strings.ContainsRune(flags["config"], 0) || !skillIdentifier.MatchString(flags["id"]) {
		return usage()
	}
	var tasks []string
	maxCost := 0.0
	if generate {
		if !generationModelIdentifier.MatchString(flags["model"]) || !skillIdentifier.MatchString(flags["name"]) {
			return usage()
		}
		tasks = strings.Split(flags["tasks"], ",")
		if len(tasks) < 2 || len(tasks) > 20 {
			return usage()
		}
		seen := map[string]bool{}
		for _, id := range tasks {
			if !skillIdentifier.MatchString(id) || seen[id] {
				return usage()
			}
			seen[id] = true
		}
		if text, exists := flags["max-cost"]; exists {
			var err error
			maxCost, err = strconv.ParseFloat(text, 64)
			if err != nil || maxCost < 0 || math.IsNaN(maxCost) || math.IsInf(maxCost, 0) {
				return usage()
			}
		}
	}
	cfg, err := config.Load(config.Options{ProjectFile: flags["config"], Env: config.Environment(os.Environ())})
	if err != nil {
		fmt.Fprintln(stderr, "skill generation configuration unavailable")
		return 1
	}
	svc, err := app.NewService(cfg, os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "skill generation configuration invalid")
		return 1
	}
	ctx, stop := submissionCLIContext()
	defer stop()
	timeout := 10 * time.Second
	if generate {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var output any
	if generate {
		output, err = svc.GenerateSkillDraft(ctx, flags["id"], flags["model"], skills.Key{Scope: cfg.Skills.Scope, Name: flags["name"]}, tasks, maxCost)
	} else {
		output, err = svc.PublishSkillGeneration(ctx, flags["id"])
	}
	if err != nil {
		fmt.Fprintln(stderr, "skill generation action failed; inspect the saved attempt before retrying")
		return 1
	}
	return writeSubmissionJSON(ctx, stdout, output)
}
