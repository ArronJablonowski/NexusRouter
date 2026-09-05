package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"syscall"
	"time"

	"darwinrouter/internal/app"
	"darwinrouter/internal/config"
	"darwinrouter/internal/telemetry"
)

type summaryArgs struct {
	config, task, model string
	keep                int
	maxCost             float64
}

func parseSummaryArgs(args []string) (summaryArgs, error) {
	var out summaryArgs
	fs := flag.NewFlagSet("summary", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&out.config, "config", "", "configuration")
	fs.StringVar(&out.task, "task", "", "completed source task")
	fs.StringVar(&out.model, "model", "", "configured summary model")
	fs.IntVar(&out.keep, "keep", 0, "recent messages to retain")
	fs.Float64Var(&out.maxCost, "max-cost", 0, "estimated summary cost ceiling")
	if fs.Parse(args) != nil || fs.NArg() != 0 || out.config == "" || out.task == "" || out.model == "" || out.keep < 1 || out.keep > 100000 || out.maxCost < 0 || math.IsNaN(out.maxCost) || math.IsInf(out.maxCost, 0) {
		return out, errors.New("invalid summary arguments")
	}
	return out, nil
}

func runSummary(args []string, stdout, stderr io.Writer) int {
	parsed, err := parseSummaryArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, "usage: darwin summary --config path --task id --model id --keep n [--max-cost amount] (creates a draft only)")
		return 2
	}
	cfg, err := config.Load(config.Options{ProjectFile: parsed.config, Env: config.Environment(os.Environ())})
	if err != nil {
		fmt.Fprintln(stderr, "summary configuration unavailable")
		return 1
	}
	svc, err := app.NewService(cfg, os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "summary configuration invalid")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	attempt, err := svc.SummarizeTask(ctx, parsed.task, parsed.model, parsed.keep, parsed.maxCost)
	if err != nil {
		if attempt.ID != "" {
			if _, writeErr := fmt.Fprintln(stderr, "Summary attempt:", attempt.ID); writeErr != nil {
				return 1
			}
		}
		fmt.Fprintln(stderr, "summary failed; inspect stored summary attempts before retrying")
		return 1
	}
	if json.NewEncoder(stdout).Encode(attempt) != nil {
		return 1
	}
	return 0
}

func runSummaries(args []string, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: darwin summaries list|show --db path [--task id --after id --limit n] [--id id]")
		return 2
	}
	if len(args) == 0 || (args[0] != "list" && args[0] != "show") {
		return usage()
	}
	fs := flag.NewFlagSet("summaries", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("db", "", "existing database")
	task := fs.String("task", "", "optional source task filter")
	id := fs.String("id", "", "summary attempt ID")
	after := fs.String("after", "", "exclusive attempt ID cursor")
	limit := fs.Int("limit", 100, "page size")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *path == "" || *limit < 1 || *limit > 100 || (args[0] == "show" && (*id == "" || *task != "" || *after != "")) || (args[0] == "list" && *id != "") {
		return usage()
	}
	listFlag := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "task" || f.Name == "after" || f.Name == "limit" {
			listFlag = true
		}
	})
	if args[0] == "show" && listFlag {
		return usage()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := telemetry.OpenReadOnly(ctx, *path)
	if err != nil {
		fmt.Fprintln(stderr, "summary database unavailable")
		return 1
	}
	defer db.Close()
	var output any
	if args[0] == "show" {
		output, err = db.SummaryAttempt(ctx, *id)
	} else {
		output, err = db.ListSummaryAttempts(ctx, *task, *after, *limit)
	}
	if err != nil {
		fmt.Fprintln(stderr, "summary records unavailable")
		return 1
	}
	if json.NewEncoder(stdout).Encode(output) != nil {
		return 1
	}
	return 0
}
