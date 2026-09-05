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

	"darwinrouter/evaluation"
	"darwinrouter/internal/api"
	"darwinrouter/internal/app"
	"darwinrouter/internal/config"
	"darwinrouter/internal/telemetry"
	"darwinrouter/sessions"
)

func runServe(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("config", "", "configuration file")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *path == "" {
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
	handler, err := api.New(token, s.Workers.Max, api.Services{
		Summarize:       service.SummarizeTask,
		SummaryAttempt:  db.SummaryAttempt,
		SummaryAttempts: db.ListSummaryAttempts,
		ReviewSummary:   service.ReviewSummary,
		SummaryReviews:  db.SummaryReviews,
		FeedbackHistory: func(ctx context.Context, task string) ([]evaluation.Record, error) {
			return app.FeedbackHistory(ctx, s.Telemetry.Database, task)
		},
		ReviseFeedback: func(ctx context.Context, task, expected string, accepted bool) error {
			return app.ReviseFeedback(ctx, s.Telemetry.Database, task, expected, accepted)
		},
		Run:     service.Run,
		Inspect: func(ctx context.Context, id string) (sessions.Snapshot, error) { return sessions.Replay(ctx, db, id) },
		Health:  func(ctx context.Context) error { _, err := db.Read(ctx, "__health__", 0, 1); return err },
		Feedback: func(ctx context.Context, task string, accepted bool, cost float64) error {
			return app.RecordFeedback(ctx, s.Telemetry.Database, task, accepted, cost)
		},
	})
	if err != nil {
		fmt.Fprintln(stderr, "invalid daemon configuration")
		return 1
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		fmt.Fprintln(stderr, "cannot bind daemon address")
		return 1
	}
	defer listener.Close()
	if err := serveHTTP(ctx, listener, handler, stdout); err != nil {
		fmt.Fprintln(stderr, "daemon stopped with an error")
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
