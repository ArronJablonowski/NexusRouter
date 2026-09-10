package cli

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/approvals"
	"github.com/ArronJablonowski/DarwinRouter/daemon"
	"github.com/ArronJablonowski/DarwinRouter/evaluation"
	"github.com/ArronJablonowski/DarwinRouter/health"
	"github.com/ArronJablonowski/DarwinRouter/internal/api"
	"github.com/ArronJablonowski/DarwinRouter/internal/app"
	"github.com/ArronJablonowski/DarwinRouter/internal/browserauth"
	"github.com/ArronJablonowski/DarwinRouter/internal/browserops"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/internal/webuiapp"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/skills"
	"github.com/ArronJablonowski/DarwinRouter/webui"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

func runServe(args []string, stdout, stderr io.Writer) int {
	return runServeWithValidators(args, stdout, stderr, nil)
}

// The stock CLI supplies no executable validation policy. Embedding hosts may
// explicitly bind configured identities to trusted, cooperative callbacks.
func runServeWithValidators(args []string, stdout, stderr io.Writer, registry *skills.ValidatorRegistry) int {
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
	service, err := app.NewService(s, os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "invalid application configuration")
		return 1
	}
	learningPlan, err := app.PrepareConfiguredLearning(service, registry)
	if err != nil {
		fmt.Fprintln(stderr, "cannot prepare skill learning supervisor")
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
	workboards, err := app.NewWorkboardBridge(db, db, time.Now)
	if err != nil {
		fmt.Fprintln(stderr, "cannot initialize workboard services")
		return 1
	}
	var dispatcher *app.Dispatcher
	var learner *app.ConfiguredLearning
	var exporter *app.MetricsExporter
	var traceExporter *app.TraceExporter
	var browserHandler *webuiapp.Handler
	healthReport := func(ctx context.Context) (health.Report, error) {
		if dispatcher == nil {
			return health.Report{}, errors.New("supervisor unavailable")
		}
		report, err := service.HealthReport(ctx, dispatcher.Health())
		if err != nil {
			return health.Report{}, err
		}
		report, err = withConfiguredLearningHealth(report, learner.Health())
		if err != nil {
			return health.Report{}, err
		}
		report, err = withMetricsExportHealth(report, exporter.Health())
		if err != nil {
			return health.Report{}, err
		}
		return withTraceExportHealth(report, traceExporter.Health())
	}
	if s.WebUI.Enabled {
		operationStore, operationErr := browserops.Open(ctx, s.Telemetry.Database)
		if operationErr != nil {
			fmt.Fprintln(stderr, "cannot initialize Web UI operation journal")
			return 1
		}
		defer operationStore.Close()
		browserMutations, mutationErr := app.NewBrowserMutations(service, operationStore)
		if mutationErr != nil {
			fmt.Fprintln(stderr, "cannot initialize Web UI mutation service")
			return 1
		}
		browserWorkboards, mutationErr := app.NewBrowserWorkboardMutations(workboards, operationStore)
		if mutationErr != nil {
			fmt.Fprintln(stderr, "cannot initialize Web UI workboard mutation service")
			return 1
		}
		liveText := webuiapp.NewLiveTextHub()
		if err := app.InstallPresentationTextSink(service, liveText.Publish); err != nil {
			fmt.Fprintln(stderr, "cannot initialize Web UI presentation stream")
			return 1
		}
		sessionTTL, durationErr := config.Duration(s.WebUI.BrowserSessionTTL)
		if durationErr != nil {
			fmt.Fprintln(stderr, "invalid Web UI configuration")
			return 1
		}
		browserStore, storeErr := browserauth.New(browserauth.Options{SessionTTL: sessionTTL})
		if storeErr != nil {
			fmt.Fprintln(stderr, "cannot initialize Web UI authority")
			return 1
		}
		allowedHosts := []string{listener.Addr().String()}
		seenHosts := map[string]bool{listener.Addr().String(): true}
		for _, origin := range s.WebUI.AllowedOrigins {
			parsed, _ := url.Parse(origin)
			if parsed != nil && !seenHosts[parsed.Host] {
				allowedHosts = append(allowedHosts, parsed.Host)
				seenHosts[parsed.Host] = true
			}
		}
		cursorKey := sha256.Sum256(append([]byte("darwin-browser-stream-v1\x00"), []byte(token)...))
		browserHandler, err = webuiapp.New(webuiapp.Options{BasePath: s.WebUI.PathPrefix, AllowedHosts: allowedHosts, AllowedOrigins: s.WebUI.AllowedOrigins, Store: browserStore, LiveText: liveText, CursorKey: cursorKey[:], Mutations: webuiapp.MutationServices{
			Chat: browserMutations.Chat, Cancel: browserMutations.Cancel, Steer: browserMutations.Steer,
			TaskControls: browserMutations.TaskControls, FeedbackContext: browserMutations.FeedbackContext,
			Feedback: browserMutations.Feedback, Approvals: browserMutations.Approvals, DecideApproval: browserMutations.DecideApproval,
			Operations: browserMutations.Operations, Submission: browserMutations.Submission,
		}, Reads: webuiapp.ReadServices{
			Chats: service.ListChats, History: service.ChatHistory,
			CommittedEvents: func(ctx context.Context, options sessions.EventLogOptions) (sessions.CommittedEventPage, error) {
				return db.ReadCommittedEventPage(ctx, options)
			},
		}, Inspections: webuiapp.InspectionServices{
			Models: func(ctx context.Context) (webui.ModelInspectionPage, error) {
				report, _ := healthReport(ctx)
				return service.BrowserModels(ctx, report)
			},
			Route:  service.BrowserRoute,
			Usage:  service.BrowserTaskUsage,
			Tools:  service.BrowserTools,
			Audits: service.BrowserAudits,
			Health: func(ctx context.Context) (webui.HealthInspection, error) {
				report, reportErr := healthReport(ctx)
				return app.BrowserHealth(report, reportErr), nil
			},
			Resources: func(ctx context.Context) (webui.ResourceInspection, error) {
				return service.BrowserResources(ctx), nil
			},
		}, Workboards: webuiapp.WorkboardServices{
			List: workboards.BrowserList, Read: workboards.BrowserRead,
			Events: workboards.BrowserEvents, Mutate: browserWorkboards.Mutate,
		}})
		if err != nil {
			fmt.Fprintln(stderr, "invalid Web UI configuration")
			return 1
		}
	}
	handler, err := api.New(token, s.Workers.Max, api.Services{
		Models: func(ctx context.Context) ([]string, error) {
			if ctx == nil {
				return nil, context.Canceled
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			ids := make([]string, len(s.Models))
			for i := range s.Models {
				ids[i] = s.Models[i].ID
			}
			return ids, nil
		},
		ConfiguredModels: service.ConfiguredModelCatalog,
		ModelDeprecation: service.ModelDeprecation,
		Memory:           service.Memory,
		ExportMemory:     service.ExportMemory,
		Memories:         service.Memories,
		PutMemory:        service.PutMemory,
		DeleteMemory:     service.DeleteMemory,
		DaemonStatus: func(ctx context.Context) (daemon.Status, error) {
			status, err := control.Current(ctx)
			if err == nil && status.State == "ready" && (dispatcher == nil || dispatcher.Health().Status != "healthy" || !configuredLearningReady(learner) || metricsExportDegraded(exporter.Health()) || traceExportDegraded(traceExporter.Health())) {
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
		SubmitBranch:         service.SubmitBranch,
		SubmitResume:         service.SubmitResume,
		ResumeSubmission:     service.ResumeSubmission,
		RunSubmission:        service.RunSubmission,
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
		RunAudit:     service.RunAudit,
		InspectAudit: service.InspectAudit,
		CancelAudit:  service.CancelAudit,
		AuditEvents:  service.ReadAuditEvents,
		ReviseFeedback: func(ctx context.Context, task, expected string, accepted bool) error {
			return app.ReviseFeedback(ctx, s.Telemetry.Database, task, expected, accepted)
		},
		RunStream:             service.RunStream,
		RunTextStream:         service.RunTextStream,
		Events:                db.ReadEventPage,
		SubmissionStream:      db.ReadSubmissionStreamPage,
		Run:                   service.Run,
		Inspect:               db.TaskSnapshot,
		TaskUsage:             service.InspectTaskUsage,
		TaskContinuation:      db.TaskContinuation,
		RouteExplanation:      db.RouteExplanation,
		Tasks:                 service.ListTasks,
		SessionTasks:          service.ListSessionTasks,
		SkillTaskOutcome:      service.SkillTaskOutcome,
		CompareSkillOutcomes:  service.CompareSkillOutcomes,
		SelectSkillComparison: service.SelectSkillComparison,
		TaskLeases: func(ctx context.Context, task string) (workers.TaskLeaseStatus, error) {
			return app.InspectTaskLeases(ctx, s.Telemetry.Database, task)
		},
		ScopeLeases: func(ctx context.Context, scope string) (workers.ScopeLeaseStatus, error) {
			return app.InspectScopeLeases(ctx, s.Telemetry.Database, scope)
		},
		LeaseAttention: func(ctx context.Context, options workers.LeaseAttentionOptions) (workers.LeaseAttentionPage, error) {
			return app.InspectLeaseAttention(ctx, s.Telemetry.Database, options)
		},
		LeaseAttentionHistory: func(ctx context.Context, id string, options workers.LeaseAttentionHistoryOptions) (workers.LeaseAttentionHistoryPage, error) {
			return app.InspectLeaseAttentionHistory(ctx, s.Telemetry.Database, id, options)
		},
		Health: func(ctx context.Context) error {
			if dispatcher == nil || dispatcher.Health().Status != "healthy" || !configuredLearningReady(learner) {
				return errors.New("supervisor unavailable")
			}
			_, err := db.Read(ctx, "__health__", 0, 1)
			return err
		},
		HealthReport: healthReport,
		Feedback: func(ctx context.Context, task string, accepted bool, cost float64) error {
			return app.RecordFeedback(ctx, s.Telemetry.Database, task, accepted, cost)
		},
		ApproveBrowserChallenge: func(ctx context.Context, id, code string) error {
			if browserHandler == nil || ctx == nil || ctx.Err() != nil {
				return browserauth.ErrInvalid
			}
			return browserHandler.ApproveChallenge(id, code)
		},
		WorkboardList:   workboards.NativeList,
		WorkboardRead:   workboards.NativeRead,
		WorkboardMutate: workboards.NativeMutate,
		WorkboardEvents: workboards.NativeEvents,
	})
	if err != nil {
		fmt.Fprintln(stderr, "invalid daemon configuration")
		return 1
	}
	learner, err = learningPlan.Start(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "cannot start skill learning supervisor")
		return 1
	}
	defer learner.Close()
	dispatcher, err = app.StartDispatcher(ctx, service)
	if err != nil {
		fmt.Fprintln(stderr, "cannot start task dispatcher")
		return 1
	}
	defer dispatcher.Close()
	exporter, err = app.StartConfiguredMetricsExport(ctx, service)
	if err != nil {
		fmt.Fprintln(stderr, "cannot start metrics exporter")
		return 1
	}
	defer exporter.Close()
	traceExporter, err = app.StartConfiguredTraceExport(ctx, service)
	if err != nil {
		fmt.Fprintln(stderr, "cannot start trace exporter")
		return 1
	}
	defer traceExporter.Close()
	rootHandler := composeServeHandler(handler, browserHandler, s.WebUI.PathPrefix)
	if err := serveHTTP(ctx, listener, rootHandler, stdout); err != nil {
		fmt.Fprintln(stderr, "daemon stopped with an error")
		return 1
	}
	if err := exporter.Close(); err != nil {
		fmt.Fprintln(stderr, "metrics exporter requires inspection")
		return 1
	}
	if err := traceExporter.Close(); err != nil {
		fmt.Fprintln(stderr, "trace exporter requires inspection")
		return 1
	}
	if err := learner.Close(); err != nil {
		fmt.Fprintln(stderr, "skill learning supervisor requires inspection")
		return 1
	}
	if err := dispatcher.Close(); err != nil {
		fmt.Fprintln(stderr, "task dispatcher requires inspection")
		return 1
	}
	return 0
}

type browserMount struct {
	basePath string
	browser  http.Handler
	native   http.Handler
}

func composeServeHandler(native, browser http.Handler, basePath string) http.Handler {
	if browser == nil {
		return native
	}
	return browserMount{basePath: basePath, browser: browser, native: native}
}

func (m browserMount) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL != nil && (request.URL.Path == m.basePath || strings.HasPrefix(request.URL.Path, m.basePath+"/")) {
		m.browser.ServeHTTP(writer, request)
		return
	}
	m.native.ServeHTTP(writer, request)
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
