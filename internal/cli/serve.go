package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/daemon"
	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/health"
	"github.com/ArronJablonowski/DarwinRouter/internal/api"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

func runServe(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("config", "", "configuration file")
	instance := fs.String("instance-id", "", "managed launch identity")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *path == "" || (*instance != "" && !daemon.ValidID(*instance)) {
		fmt.Fprintln(stderr, "usage: darwin serve --config path (requires DARWIN_API_TOKEN)")
		return 2
	}
	s, err := config.Load(config.Options{ProjectFile: *path, Env: config.Environment(os.Environ())})
	if err != nil {
		fmt.Fprintln(stderr, "cannot load daemon configuration")
		return 1
	}
	host, port, err := net.SplitHostPort(s.Daemon.Listen)
	if err != nil {
		fmt.Fprintln(stderr, "invalid daemon address")
		return 1
	}
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		fmt.Fprintln(stderr, "daemon currently requires loopback binding")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	token := os.Getenv("DARWIN_API_TOKEN")
	if len(token) < 32 {
		fmt.Fprintln(stderr, "DARWIN_API_TOKEN must contain at least 32 characters")
		return 1
	}
	// Binding is the single-instance gate. Do not migrate or dispatch against
	// storage if another process already owns this endpoint.
	listener, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		fmt.Fprintln(stderr, "cannot bind daemon address")
		return 1
	}
	defer listener.Close()
	if *instance == "" {
		*instance = daemon.NewID()
	}
	control, err := daemon.New(*instance, stop)
	if err != nil {
		fmt.Fprintln(stderr, "cannot initialize daemon identity")
		return 1
	}
	db, err := telemetry.Open(ctx, s.Telemetry.Database)
	if err != nil {
		fmt.Fprintln(stderr, "cannot open daemon storage")
		return 1
	}
	defer db.Close()
	service, err := app.NewService(s, os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "invalid application configuration")
		return 1
	}
	var dispatcher *app.Dispatcher
	handler, err := api.New(token, s.Workers.Max, api.Services{
		ModelDeprecation: service.ModelDeprecation,
		Memory:           service.Memory,
		Memories:         service.Memories,
		PutMemory:        service.PutMemory,
		DeleteMemory:     service.DeleteMemory,
		DaemonStatus: func(ctx context.Context) (daemon.Status, error) {
			status, err := control.Current(ctx)
			if err == nil && status.State == "ready" && (dispatcher == nil || dispatcher.Health().Status != "healthy") {
				// Keep identity visible so an operator can stop a degraded daemon.
				status.State = "degraded"
			}
			return status, err
		},
		StopDaemon:             control.Stop,
		DiscoverSkillWorkflows: service.DiscoverSkillWorkflows,
		GenerateSkillDraft: func(ctx context.Context, id, model, name string, tasks []string, maxCost float64) (skills.GenerationAttempt, error) {
			return service.GenerateSkillDraft(ctx, id, model, skills.Key{Scope: s.Skills.Scope, Name: name}, tasks, maxCost)
		},
		PublishSkillGeneration: service.PublishSkillGeneration,
		SkillGeneration: func(ctx context.Context, scope, id string) (skills.GenerationAttempt, error) {
			return app.InspectSkillGeneration(ctx, s.Telemetry.Database, scope, id)
		},
		SkillGenerations: func(ctx context.Context, scope, after string, limit int) ([]skills.GenerationSummary, error) {
			return app.ListSkillGenerations(ctx, s.Telemetry.Database, scope, after, limit)
		},
		ApprovalExecution: func(ctx context.Context, task, id string) (approvals.ExecutionStatus, error) {
			return app.ApprovalExecutionStatus(ctx, s.Telemetry.Database, task, id)
		},
		DecideApproval: func(ctx context.Context, command approvals.Command) (approvals.Record, error) {
			return service.DecideApproval(ctx, command, "api_operator")
		},
		Approval: func(ctx context.Context, task, id string) (approvals.Record, error) {
			return app.InspectApproval(ctx, s.Telemetry.Database, task, id)
		},
		Approvals: func(ctx context.Context, opts approvals.ListOptions) (approvals.Page, error) {
			return app.ListApprovals(ctx, s.Telemetry.Database, opts)
		},
		Metrics:              service.Metrics,
		Submit:               service.Submit,
		Submissions:          service.ListSubmissions,
		SubmissionRecoveries: service.SubmissionRecoveries,
		Submission:           service.SubmissionStatus,
		CancelSubmission:     service.CancelSubmission,
		Cancel:               service.CancelTask,
		Cancellation:         service.CancellationStatus,
		Steer:                service.SteerTask,
		Steering:             service.SteeringStatus,
		SteeringList:         service.ListSteering,
		Summarize:            service.SummarizeTask,
		SummaryAttempt:       db.SummaryAttempt,
		SummaryAttempts:      db.ListSummaryAttempts,
		ReviewSummary:        service.ReviewSummary,
		SummaryReviews:       db.SummaryReviews,
		FeedbackHistory: func(ctx context.Context, task string) ([]evaluation.Record, error) {
			return app.FeedbackHistory(ctx, s.Telemetry.Database, task)
		},
		ReviseFeedback: func(ctx context.Context, task, expected string, accepted bool) error {
			return app.ReviseFeedback(ctx, s.Telemetry.Database, task, expected, accepted)
		},
		RunStream:        service.RunStream,
		RunTextStream:    service.RunTextStream,
		Events:           db.ReadEventPage,
		Run:              service.Run,
		Inspect:          db.TaskSnapshot,
		TaskContinuation: db.TaskContinuation,
		Health: func(ctx context.Context) error {
			if dispatcher == nil || dispatcher.Health().Status != "healthy" {
				return errors.New("supervisor unavailable")
			}
			_, err := db.Read(ctx, "__health__", 0, 1)
			return err
		},
		HealthReport: func(ctx context.Context) (health.Report, error) {
			if dispatcher == nil {
				return health.Report{}, errors.New("supervisor unavailable")
			}
			return service.HealthReport(ctx, dispatcher.Health())
		},
		Feedback: func(ctx context.Context, task string, accepted bool, cost float64) error {
			return app.RecordFeedback(ctx, s.Telemetry.Database, task, accepted, cost)
		},
	})
	if err != nil {
		fmt.Fprintln(stderr, "invalid daemon configuration")
		return 1
	}
	dispatcher, err = app.StartDispatcher(ctx, service)
	if err != nil {
		fmt.Fprintln(stderr, "cannot start task dispatcher")
		return 1
	}
	defer dispatcher.Close()
	if err := serveHTTP(ctx, listener, handler, stdout); err != nil {
		fmt.Fprintln(stderr, "daemon stopped with an error")
		return 1
	}
	if err := dispatcher.Close(); err != nil {
		fmt.Fprintln(stderr, "task dispatcher requires inspection")
		return 1
	}
	return 0
}

func serveHTTP(ctx context.Context, listener net.Listener, handler http.Handler, stdout io.Writer) error {
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 5*time.Minute + 15*time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(io.Discard, "", 0), BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	if _, err := fmt.Fprintln(stdout, "DarwinRouter listening on", listener.Addr()); err != nil {
		server.Close()
		<-done
		return err
	}
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := server.Shutdown(shutdown)
		if err != nil {
			server.Close()
		}
		<-done
		return err
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
