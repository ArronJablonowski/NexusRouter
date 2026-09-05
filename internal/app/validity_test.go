package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"darwinrouter/internal/telemetry"
	"darwinrouter/routing"
	"darwinrouter/runtime"
)

func TestBlankAnswerChangesNextAutomaticRouteWithoutInventedMetrics(t *testing.T) {
	svc, cfg := autoFixture(t)
	ctx := context.Background()
	blank := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"message":{"content":"  "},"done":true,"done_reason":"stop"}`)
	}))
	defer blank.Close()
	seedCfg := cfg
	seedCfg.Providers = append(seedCfg.Providers[:0:0], cfg.Providers...)
	seedCfg.Providers[0].Endpoint = blank.URL
	out, err := RunExplicit(ctx, seedCfg, Request{ModelID: "a", Prompt: "hello", Domain: "creative"}, nil)
	if !errors.Is(err, runtime.ErrEmptyOutput) {
		t.Fatal(out, err)
	}
	db, err := telemetry.OpenReadOnly(ctx, cfg.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key := routing.Key{Model: "a", Provider: "local", Domain: "creative", Profile: "default"}
	v, err := db.OutputValidity(ctx, key)
	if err != nil || v.Samples != 1 || v.Failures != 1 {
		t.Fatal(v, err)
	}
	if _, err := db.Fitness(ctx, key); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("invented execution metrics", err)
	}
	svc.settings.Evaluation.Judge = false // Objective validity is not a judge opinion.
	chosen, err := svc.Run(ctx, Request{ModelID: "auto", Prompt: "hello", Domain: "creative"})
	if err != nil || chosen.Text != "z" {
		t.Fatal(chosen, err)
	}
	other, err := svc.Run(ctx, Request{ModelID: "auto", Prompt: "hello", Domain: "code"})
	if err != nil || other.Text != "a" {
		t.Fatal("validity leaked across domains", other, err)
	}
}
