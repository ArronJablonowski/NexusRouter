package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
)

func runFeedbackRevision(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("feedback", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("db", "", "database")
	task := fs.String("task", "", "completed task")
	expected := fs.String("expected", "", "prior evaluation ID")
	outcome := fs.String("outcome", "", "accepted or rejected")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *path == "" || *task == "" || (args[0] == "revise" && (*expected == "" || (*outcome != "accepted" && *outcome != "rejected"))) || (args[0] == "show" && (*expected != "" || *outcome != "")) {
		fmt.Fprintln(stderr, "usage: darwin feedback show|revise --db path --task id [--expected evaluation-id --outcome accepted|rejected]")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if args[0] == "show" {
		history, err := app.FeedbackHistory(ctx, *path, *task)
		if err != nil {
			fmt.Fprintln(stderr, "feedback history unavailable")
			return 1
		}
		if json.NewEncoder(stdout).Encode(history) != nil {
			return 1
		}
		return 0
	}
	if app.ReviseFeedback(ctx, *path, *task, *expected, *outcome == "accepted") != nil {
		fmt.Fprintln(stderr, "feedback revision rejected; inspect current history and evidence")
		return 1
	}
	if _, err := fmt.Fprintln(stdout, "Feedback revision recorded."); err != nil {
		return 1
	}
	return 0
}
