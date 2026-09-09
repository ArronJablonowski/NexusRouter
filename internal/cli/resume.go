package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func parseResumeArgs(args []string) (config.Options, app.Request, string, sessions.TaskHeadFence, error) {
	return parseBranchArgs(args)
}

func runResume(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	options, request, key, source, err := parseResumeArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, "usage: darwin resume --config path --key idempotency-key --task id --session id --sequence n --event id --model id < prompt.txt")
		return 2
	}
	ctx, cancel := submissionCLIContext()
	defer cancel()
	options.Env = config.Environment(os.Environ())
	settings, err := config.Load(options)
	if err != nil {
		fmt.Fprintln(stderr, "cannot load resume configuration")
		return 1
	}
	prompt, err := readTaskPrompt(ctx, stdin)
	if err != nil {
		fmt.Fprintln(stderr, "cannot read UTF-8 prompt (maximum 1 MiB; resume deadline 30 seconds)")
		return 1
	}
	request.Prompt = prompt
	svc, err := app.NewService(settings, os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "invalid application configuration")
		return 1
	}
	status, err := svc.SubmitResume(ctx, key, source, request)
	if err != nil {
		fmt.Fprintln(stderr, "resume intake failed; retry only with the same key, source fence, and request")
		return 1
	}
	return writeSubmissionJSON(ctx, stdout, status)
}
