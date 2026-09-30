package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

func runSkillTaskOutcome(args []string, stdout, stderr io.Writer) int {
	flags, err := parseSteeringFlags(args, "config", "task")
	if err != nil || !sessions.ValidEventPageID(flags["task"]) {
		fmt.Fprintln(stderr, "usage: nexus task skill-outcome --config path --task id")
		return 2
	}
	cfg, err := config.Load(config.Options{ProjectFile: flags["config"], Env: config.Environment(os.Environ())})
	if err != nil {
		return skillsError(stderr, "skill outcome: configuration unavailable", 1)
	}
	svc, err := app.NewService(cfg, os.Getenv)
	if err != nil {
		return skillsError(stderr, "skill outcome: configuration unavailable", 1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := svc.SkillTaskOutcome(ctx, flags["task"])
	if err != nil {
		return skillsError(stderr, "skill outcome: evidence unavailable", 1)
	}
	return writeSubmissionJSON(ctx, stdout, out)
}
