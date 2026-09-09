package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
)

func parseBranchArgs(args []string) (config.Options, app.Request, string, sessions.TaskHeadFence, error) {
	bad := func() (config.Options, app.Request, string, sessions.TaskHeadFence, error) {
		return config.Options{}, app.Request{}, "", sessions.TaskHeadFence{}, errors.New("invalid branch arguments")
	}
	values := map[string]string{}
	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, equals := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "---") || arg == "--" {
			return bad()
		}
		switch name {
		case "task", "session", "sequence", "event":
			if _, exists := values[name]; exists {
				return bad()
			}
			if !equals {
				i++
				if i >= len(args) {
					return bad()
				}
				value = args[i]
			}
			values[name] = value
		default:
			filtered = append(filtered, arg)
			if !equals && name != "local-required" {
				i++
				if i >= len(args) {
					return bad()
				}
				filtered = append(filtered, args[i])
			}
		}
	}
	options, request, key, err := parseSubmitArgs(filtered)
	if err != nil || len(values) != 4 || request.ContinueTaskID != "" || request.Compaction != nil || request.SummaryAttemptID != "" {
		return bad()
	}
	sequence, err := strconv.ParseInt(values["sequence"], 10, 64)
	if err != nil || strconv.FormatInt(sequence, 10) != values["sequence"] {
		return bad()
	}
	fence := sessions.TaskHeadFence{Version: 1, TaskID: values["task"], SessionID: values["session"], HeadSequence: sequence, HeadEventID: values["event"]}
	if fence.Validate() != nil {
		return bad()
	}
	return options, request, key, fence, nil
}

func runBranch(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	options, request, key, source, err := parseBranchArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, "usage: darwin branch --config path --key idempotency-key --task id --session id --sequence n --event id --model id < prompt.txt")
		return 2
	}
	ctx, cancel := submissionCLIContext()
	defer cancel()
	options.Env = config.Environment(os.Environ())
	settings, err := config.Load(options)
	if err != nil {
		fmt.Fprintln(stderr, "cannot load branch configuration")
		return 1
	}
	prompt, err := readTaskPrompt(ctx, stdin)
	if err != nil {
		fmt.Fprintln(stderr, "cannot read UTF-8 prompt (maximum 1 MiB; branch deadline 30 seconds)")
		return 1
	}
	request.Prompt = prompt
	svc, err := app.NewService(settings, os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "invalid application configuration")
		return 1
	}
	status, err := svc.SubmitBranch(ctx, key, source, request)
	if err != nil {
		fmt.Fprintln(stderr, "branch intake failed; retry only with the same key, source fence, and request")
		return 1
	}
	return writeSubmissionJSON(ctx, stdout, status)
}
