package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
)

func runTaskInspection(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "list" {
		return runTaskList(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "skill-outcome" {
		return runSkillTaskOutcome(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "leases" {
		return runTaskLeases(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "continuation" {
		return runTaskContinuation(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "route" {
		return runTaskRoute(args[1:], stdout, stderr)
	}
	if len(args) == 0 || args[0] != "show" {
		fmt.Fprintln(stderr, "usage: nexus task list|show|continuation|route|leases --db path")
		return 2
	}
	fs := flag.NewFlagSet("task show", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("db", "", "existing database")
	task := fs.String("task", "", "task ID")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *path == "" || *task == "" {
		fmt.Fprintln(stderr, "usage: nexus task show --db path --task id")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	snapshot, err := app.InspectTask(ctx, *path, *task)
	if err != nil {
		fmt.Fprintln(stderr, "task history missing, incomplete or invalid")
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if encoder.Encode(snapshot) != nil {
		return 1
	}
	return 0
}

func runTaskRoute(args []string, stdout, stderr io.Writer) int {
	flags, err := parseSteeringFlags(args, "db", "task")
	if err != nil {
		fmt.Fprintln(stderr, "usage: nexus task route --db path --task id")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	explanation, err := app.InspectRouteExplanation(ctx, flags["db"], flags["task"])
	if err != nil {
		fmt.Fprintln(stderr, "task route missing, incomplete or invalid")
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if encoder.Encode(explanation) != nil {
		return 1
	}
	return 0
}

func runTaskContinuation(args []string, stdout, stderr io.Writer) int {
	flags, err := parseSteeringFlags(args, "db", "task")
	if err != nil {
		fmt.Fprintln(stderr, "usage: nexus task continuation --db path --task id")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	status, err := app.InspectTaskContinuation(ctx, flags["db"], flags["task"])
	if err != nil {
		fmt.Fprintln(stderr, "task history missing, incomplete or invalid")
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if encoder.Encode(status) != nil {
		return 1
	}
	return 0
}
