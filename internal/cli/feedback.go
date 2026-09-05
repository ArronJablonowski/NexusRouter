package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math"
	"time"

	"darwinrouter/internal/app"
)

func runFeedback(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && (args[0] == "show" || args[0] == "revise") {
		return runFeedbackRevision(args, stdout, stderr)
	}
	fs := flag.NewFlagSet("feedback", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	db := fs.String("db", "", "existing task database")
	task := fs.String("task", "", "completed task")
	outcome := fs.String("outcome", "", "accepted or rejected")
	cost := fs.Float64("attempt-cost", -1, "observed final model-attempt cost")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *db == "" || *task == "" || (*outcome != "accepted" && *outcome != "rejected") || *cost < 0 || math.IsNaN(*cost) || math.IsInf(*cost, 0) {
		fmt.Fprintln(stderr, "usage: darwin feedback --db path --task id --outcome accepted|rejected --attempt-cost amount")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if app.RecordFeedback(ctx, *db, *task, *outcome == "accepted", *cost) != nil {
		fmt.Fprintln(stderr, "feedback rejected; inspect task completion and existing evaluation")
		return 1
	}
	if _, err := fmt.Fprintln(stdout, "Feedback recorded (identical retries do not add samples)."); err != nil {
		return 1
	}
	return 0
}
