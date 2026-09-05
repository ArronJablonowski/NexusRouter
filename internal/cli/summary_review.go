package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"darwinrouter/internal/app"
	"darwinrouter/internal/config"
)

type summaryReviewArgs struct{ config, attempt, expected, decision, note string }

func validSummaryReviewLabel(value string) bool {
	return len(value) <= 128 && strings.TrimSpace(value) == value && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}

func parseSummaryReviewArgs(args []string) (summaryReviewArgs, error) {
	var out summaryReviewArgs
	fs := flag.NewFlagSet("summary-review", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&out.config, "config", "", "configuration")
	fs.StringVar(&out.attempt, "attempt", "", "stored summary attempt ID")
	fs.StringVar(&out.expected, "expected", "", "current review ID for a revision")
	fs.StringVar(&out.decision, "decision", "", "approved or rejected")
	fs.StringVar(&out.note, "note", "", "operator review explanation")
	if fs.Parse(args) != nil || fs.NArg() != 0 || out.config == "" || out.attempt == "" || !validSummaryReviewLabel(out.attempt) || !validSummaryReviewLabel(out.expected) || (out.decision != "approved" && out.decision != "rejected") || strings.TrimSpace(out.note) == "" || len(out.note) > 4096 || !utf8.ValidString(out.note) {
		return out, errors.New("invalid summary review arguments")
	}
	return out, nil
}

func runSummaryReview(args []string, stdout, stderr io.Writer) int {
	parsed, err := parseSummaryReviewArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, "usage: darwin summary-review --config path --attempt id [--expected review-id] --decision approved|rejected --note text")
		return 2
	}
	cfg, err := config.Load(config.Options{ProjectFile: parsed.config, Env: config.Environment(os.Environ())})
	if err != nil {
		fmt.Fprintln(stderr, "summary review configuration unavailable")
		return 1
	}
	svc, err := app.NewService(cfg, os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "summary review configuration invalid")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	review, err := svc.ReviewSummary(ctx, parsed.attempt, parsed.expected, parsed.decision, parsed.note)
	if err != nil {
		fmt.Fprintln(stderr, "summary review rejected; inspect review history before retrying")
		return 1
	}
	if json.NewEncoder(stdout).Encode(review) != nil {
		return 1
	}
	return 0
}

func runSummaryReviews(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("summary-reviews", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("db", "", "existing database")
	attempt := fs.String("attempt", "", "summary attempt ID")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *path == "" || *attempt == "" || !validSummaryReviewLabel(*attempt) {
		fmt.Fprintln(stderr, "usage: darwin summary-reviews --db path --attempt id")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reviews, err := app.SummaryReviewHistory(ctx, *path, *attempt)
	if err != nil {
		fmt.Fprintln(stderr, "summary review history unavailable")
		return 1
	}
	if json.NewEncoder(stdout).Encode(reviews) != nil {
		return 1
	}
	return 0
}
