package cli

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/runtime"
)

type chatHooks struct {
	Run             taskStreamRunner
	RunLive         func(context.Context, app.Request, func(runtime.Event) error, func(string) error) (app.Result, error)
	Approvals       <-chan chatApprovalRequest
	Steer           func(context.Context, string, string, string) (runtime.SteeringMessage, error)
	Feedback        func(context.Context, string, bool, float64) error
	FeedbackHistory func(context.Context, string) ([]evaluation.Record, error)
	ReviseFeedback  func(context.Context, string, string, bool) error
}

type chatLine struct {
	Text string
	Err  error
}

func runChat(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	options, request, jsonMode, err := parseRunOptions(args)
	if err != nil || jsonMode {
		_, _ = io.WriteString(stderr, "darwin: invalid chat arguments; use --config and --model (JSON mode uses run)\n")
		return 2
	}
	options.Env = config.Environment(os.Environ())
	settings, err := config.Load(options)
	if err != nil {
		_, _ = io.WriteString(stderr, "darwin: invalid chat configuration\n")
		return 1
	}
	var service *app.Service
	var approvalRequests chan chatApprovalRequest
	if settings.Tools.CreateEnabled || settings.Tools.ReplaceEnabled {
		if !chatReviewTerminal(stdin) || !chatReviewTerminal(stdout) {
			_, _ = io.WriteString(stderr, "darwin: file write review requires terminal input and output\n")
			return 1
		}
		approvalRequests = make(chan chatApprovalRequest)
		service, err = app.NewServiceWithToolApproval(settings, os.Getenv, nil, nil, nil, nil, nil, newChatReviewer(settings, os.Getenv, approvalRequests))
	} else {
		service, err = app.NewService(settings, os.Getenv)
	}
	if err != nil {
		_, _ = io.WriteString(stderr, "darwin: chat service unavailable\n")
		return 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	brokenPipe := make(chan os.Signal, 1)
	signal.Notify(brokenPipe, syscall.SIGPIPE)
	defer signal.Stop(brokenPipe)
	hooks := chatHooks{
		RunLive: service.RunLiveStream, Steer: service.SteerTask,
		Approvals: approvalRequests,
		Feedback: func(ctx context.Context, task string, accepted bool, cost float64) error {
			return app.RecordFeedback(ctx, settings.Telemetry.Database, task, accepted, cost)
		},
		FeedbackHistory: func(ctx context.Context, task string) ([]evaluation.Record, error) {
			return app.FeedbackHistory(ctx, settings.Telemetry.Database, task)
		},
		ReviseFeedback: func(ctx context.Context, task, expected string, accepted bool) error {
			return app.ReviseFeedback(ctx, settings.Telemetry.Database, task, expected, accepted)
		},
	}
	return runChatIO(ctx, request, hooks, stdin, stdout, signals)
}

func runChatIO(ctx context.Context, request app.Request, hooks chatHooks, stdin io.Reader, stdout io.Writer, signals <-chan os.Signal) (code int) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	input, closeInput, err := prepareChatInput(ctx, stdin)
	if err != nil {
		return 1
	}
	defer func() {
		if closeInput() != nil {
			code = 1
		}
	}()
	output, closeOutput, err := prepareStreamOutput(ctx, stdout)
	if err != nil {
		return 1
	}
	defer func() {
		if closeOutput() != nil {
			code = 1
		}
	}()
	lines := make(chan chatLine, 1)
	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		defer close(lines)
		reader := bufio.NewReaderSize(input, runtime.MaxSteeringBytes+2)
		for {
			text, err := readChatLine(reader)
			select {
			case lines <- chatLine{Text: text, Err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() { cancel(); <-inputDone }()
	return runChatSession(ctx, request, hooks, lines, signals, output)
}
