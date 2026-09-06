package v1_test

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/ArronJablonowski/DarwinRouter/sdk/v1"
)

func TestPublicSummaryUninitializedAndContextBoundaries(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-created.db")
	client, err := v1.New(v1.ConfigOptions{Overrides: map[string]string{"telemetry.database": missing}})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	operations := []struct {
		name string
		call func(*v1.Client, context.Context) error
	}{
		{"generate", func(c *v1.Client, ctx context.Context) error {
			_, e := c.SummarizeTask(ctx, "task", "model", 1, 0)
			return e
		}},
		{"inspect", func(c *v1.Client, ctx context.Context) error {
			_, e := c.InspectSummaryAttempt(ctx, "attempt")
			return e
		}},
		{"list", func(c *v1.Client, ctx context.Context) error {
			_, e := c.ListSummaryAttempts(ctx, "task", "", 25)
			return e
		}},
		{"review", func(c *v1.Client, ctx context.Context) error {
			_, e := c.ReviewSummary(ctx, "attempt", "", "approved", "Compared against source")
			return e
		}},
		{"history", func(c *v1.Client, ctx context.Context) error {
			_, e := c.SummaryReviewHistory(ctx, "attempt")
			return e
		}},
	}
	for _, op := range operations {
		t.Run(op.name, func(t *testing.T) {
			for _, c := range []*v1.Client{nil, {}} {
				if e := op.call(c, context.Background()); !errors.Is(e, v1.ErrAdmission) {
					t.Fatalf("uninitialized client: %v", e)
				}
			}
			if e := op.call(client, nil); !errors.Is(e, v1.ErrAdmission) {
				t.Fatal("nil context did not return admission error")
			}
			if e := op.call(client, canceled); !errors.Is(e, context.Canceled) {
				t.Fatal("canceled context did not preserve cancellation")
			}
			if e := op.call(client, context.Background()); e == nil {
				t.Fatal("missing store accepted")
			} else if strings.Contains(e.Error(), missing) || strings.Contains(e.Error(), "SQLite") {
				t.Fatal("missing store error exposed implementation detail")
			}
		})
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("summary operation created missing storage")
	}
}

func TestPublicSummaryInvalidInputsDoNotCreateStorage(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.db")
	client, err := v1.New(v1.ConfigOptions{Overrides: map[string]string{"telemetry.database": missing}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	check := func(err error) {
		t.Helper()
		if err == nil {
			t.Fatal("invalid summary input accepted")
		}
		if strings.Contains(err.Error(), "private-invalid") {
			t.Fatal("input echoed by summary error")
		}
	}
	for _, id := range []string{"", strings.Repeat("x", 129), "private-invalid\x00identifier"} {
		_, err = client.InspectSummaryAttempt(ctx, id)
		check(err)
		_, err = client.SummaryReviewHistory(ctx, id)
		check(err)
		_, err = client.ReviewSummary(ctx, id, "", "approved", "review")
		check(err)
		_, err = client.ListSummaryAttempts(ctx, id, "", 25)
		check(err)
		_, err = client.SummarizeTask(ctx, id, "model", 1, 0)
		check(err)
	}
	for _, limit := range []int{-1, 0, 101} {
		_, err = client.ListSummaryAttempts(ctx, "task", "", limit)
		check(err)
	}
	for _, after := range []string{strings.Repeat("x", 129), "private-invalid\x00cursor"} {
		_, err = client.ListSummaryAttempts(ctx, "task", after, 25)
		check(err)
	}
	for _, keep := range []int{-1, 0, 100001} {
		_, err = client.SummarizeTask(ctx, "task", "model", keep, 0)
		check(err)
	}
	for _, budget := range []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		_, err = client.SummarizeTask(ctx, "task", "model", 1, budget)
		check(err)
	}
	for _, decision := range []string{"", "APPROVED", "private-invalid"} {
		_, err = client.ReviewSummary(ctx, "attempt", "", decision, "review")
		check(err)
	}
	_, err = client.ReviewSummary(ctx, "attempt", strings.Repeat("x", 129), "approved", "review")
	check(err)
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid summary operation created storage")
	}
}
