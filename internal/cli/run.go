package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"darwinrouter/internal/app"
	"darwinrouter/internal/config"
)

func runTask(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	project := fs.String("config", "", "project configuration")
	user := fs.String("user-config", "", "user configuration")
	model := fs.String("model", "", "configured model ID")
	previous := fs.String("continue-task", "", "completed task history to continue")
	values := overrides{}
	fs.Var(values, "set", "override scalar setting")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *project == "" || *model == "" {
		fmt.Fprintln(stderr, "usage: darwin run --config path --model id < prompt.txt")
		return 2
	}
	s, err := config.Load(config.Options{UserFile: *user, ProjectFile: *project, Env: config.Environment(os.Environ()), Flags: values})
	if err != nil {
		fmt.Fprintln(stderr, "cannot load task configuration")
		return 1
	}
	prompt, err := io.ReadAll(io.LimitReader(stdin, (1<<20)+1))
	if err != nil || len(prompt) > 1<<20 {
		fmt.Fprintln(stderr, "cannot read prompt (maximum 1 MiB)")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	service, err := app.NewService(s, os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "invalid application configuration")
		return 1
	}
	result, err := service.Run(ctx, app.Request{ModelID: *model, Prompt: string(prompt), ContinueTaskID: *previous})
	if result.TaskID != "" {
		if _, writeErr := fmt.Fprintln(stderr, "Task:", result.TaskID); writeErr != nil {
			return 1
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "task failed; inspect local task history before retrying")
		return 1
	}
	if _, err := fmt.Fprintln(stdout, result.Text); err != nil {
		return 1
	}
	return 0
}
