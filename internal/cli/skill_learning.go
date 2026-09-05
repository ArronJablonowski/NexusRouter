package cli

import (
	"context"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
)

func runSkillLearning(args []string, stdout, stderr io.Writer) int {
	usage := func() int { return skillsError(stderr, "usage: darwin skills learning status --config path", 2) }
	if len(args) < 2 || args[0] != "status" {
		return usage()
	}
	path := ""
	if len(args) == 3 && args[1] == "--config" {
		path = args[2]
	} else if len(args) == 2 && strings.HasPrefix(args[1], "--config=") {
		path = strings.TrimPrefix(args[1], "--config=")
	}
	if path == "" || strings.HasPrefix(path, "--") {
		return usage()
	}
	cfg, err := config.Load(config.Options{ProjectFile: path, Env: config.Environment(os.Environ())})
	if err != nil {
		return skillsError(stderr, "learning: configuration unavailable", 1)
	}
	service, err := app.NewService(cfg, os.Getenv)
	if err != nil {
		return skillsError(stderr, "learning: configuration unavailable", 1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	state, err := service.SkillLearningState(ctx)
	if err != nil {
		return skillsError(stderr, "learning: persisted state unavailable", 1)
	}
	return writeSubmissionJSON(ctx, stdout, state)
}
