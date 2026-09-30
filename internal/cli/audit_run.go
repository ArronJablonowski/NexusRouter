package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

func runAudit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("config", "", "configuration")
	task := fs.String("task", "", "source task")
	model := fs.String("reviewer", "", "configured reviewer ID")
	cost := fs.Float64("max-cost", 0, "estimated review cost ceiling")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *path == "" || *task == "" || *model == "" || *cost < 0 || math.IsNaN(*cost) || math.IsInf(*cost, 0) {
		fmt.Fprintln(stderr, "usage: nexus audit --config path --task id --reviewer model-id [--max-cost amount]")
		return 2
	}
	cfg, err := config.Load(config.Options{ProjectFile: *path, Env: config.Environment(os.Environ())})
	if err != nil {
		fmt.Fprintln(stderr, "audit configuration unavailable")
		return 1
	}
	svc, err := app.NewService(cfg, os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "audit configuration invalid")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	record, err := svc.AuditTask(ctx, *task, *model, *cost)
	if err != nil {
		fmt.Fprintln(stderr, "audit failed; inspect stored audits before retrying")
		return 1
	}
	if _, err := fmt.Fprintln(stdout, "Audit:", record.ID); err != nil {
		return 1
	}
	return 0
}
