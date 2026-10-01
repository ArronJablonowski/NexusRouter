package v1_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	sdk "github.com/ArronJablonowski/NexusRouter/sdk/v1"
)

func TestSDKHarnessEvidenceCanonicalReviewAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tasks.db")
	db, err := telemetry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	identity := harness.Identity{Version: 1, Harness: "fixture", HarnessVersion: "1", AdapterVersion: "1", Provider: "local", Model: "model", ModelRevision: "weights-1", ConfigSHA256: strings.Repeat("a", 64)}
	task := harness.TaskClass{Domain: "writing", Profile: "rubric-1", Difficulty: "unknown"}
	calls := 0
	for _, id := range []string{"completed", "failed"} {
		_, _, err := runtime.RunHarness(ctx, db, runtime.HarnessRequest{TaskID: id, SessionID: id, Attribution: runtime.HarnessAttribution{Identity: identity, Task: task}, ContextTokens: 8192, MaxOutputBytes: 1024, Execute: func(context.Context) (runtime.HarnessOutput, error) {
			calls++
			if id == "failed" {
				return runtime.HarnessOutput{}, errors.New("fixture failure")
			}
			return runtime.HarnessOutput{Actual: identity, Text: "evaluated fixture output"}, nil
		}})
		if (id == "completed") != (err == nil) {
			t.Fatal(id, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ledgerDir := filepath.Join(t.TempDir(), "evidence")
	ledger, err := harness.OpenEvidenceStore(ledgerDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { ledger.Close() }()
	options := sdk.ConfigOptions{Overrides: map[string]string{"telemetry.database": path}}
	client, err := sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	cursor, copied, err := client.ReconcileHarnessEvidencePage(ctx, ledger, sdk.HarnessEvidenceCursor{})
	if err != nil || copied != 1 || cursor.After != 4 {
		t.Fatal(cursor, copied, err)
	}
	outcome, err := client.ReconcileHarnessOutcome(ctx, ledger, "completed")
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := outcome.Digest()
	ranked := func() harness.Ranked {
		now := time.Now().UTC()
		evidence, e := ledger.Snapshot(ctx, now)
		if e != nil {
			t.Fatal(e)
		}
		selection, e := harness.Select(harness.Request{Version: 1, Task: task, Mode: "local_only", ContextTokens: 8192}, harness.DefaultPolicy(), []harness.Candidate{{Identity: identity, Local: true, Available: true, Authorized: true, Compatible: true, CapacityAvailable: true, CredentialAvailable: true, ContextTokens: 8192}}, evidence, now, 0)
		if e != nil {
			t.Fatal(e)
		}
		return selection.Primary
	}
	if r := ranked(); r.PendingOutputs != 1 || r.EffectiveSamples != 0 {
		t.Fatal("execution fabricated quality", r)
	}
	review := harness.Review{Version: 1, ID: "review-1", ExecutionDigest: digest, Verdict: "passed", Method: "deterministic", MethodVersion: "fixture-rubric-v1", Reviewer: "trusted-fixture", Confidence: 1, Quality: 1, CreatedAt: time.Now().UTC()}
	wrong := review
	wrong.ExecutionDigest = strings.Repeat("b", 64)
	if err := client.ReviewHarnessOutcome(ctx, ledger, "completed", wrong); !errors.Is(err, harness.ErrConflict) {
		t.Fatal("wrong output binding", err)
	}
	for i := 0; i < 2; i++ {
		if err := client.ReviewHarnessOutcome(ctx, ledger, "completed", review); err != nil {
			t.Fatal(err)
		}
	}
	if r := ranked(); r.ConfirmedSamples != 1 || r.PendingOutputs != 0 {
		t.Fatal("duplicate vote", r)
	}
	if _, err := client.ReconcileHarnessOutcome(ctx, ledger, "failed"); err == nil {
		t.Fatal("failed lineage accepted")
	}
	if err := client.ReviewHarnessOutcome(ctx, ledger, "failed", review); err == nil {
		t.Fatal("failed lineage reviewed")
	}
	revision := review
	revision.ID = "review-2"
	revision.ExpectedHead = review.ID
	revision.Verdict = "failed"
	revision.Quality = 0
	revision.CreatedAt = time.Now().UTC()
	if err := client.ReviewHarnessOutcome(ctx, ledger, "completed", revision); err != nil {
		t.Fatal(err)
	}
	stale := revision
	stale.ID = "stale-review"
	if err := client.ReviewHarnessOutcome(ctx, ledger, "completed", stale); !errors.Is(err, harness.ErrConflict) {
		t.Fatal("stale revision", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	ledger, err = harness.OpenEvidenceStore(ledgerDir)
	if err != nil {
		t.Fatal(err)
	}
	client, err = sdk.New(options)
	if err != nil {
		t.Fatal(err)
	}
	// Replaying a formerly current review must not restore its superseded vote.
	if err := client.ReviewHarnessOutcome(ctx, ledger, "completed", review); err != nil {
		t.Fatal(err)
	}
	if r := ranked(); r.ConfirmedSamples != 1 || r.Correctness >= .5 {
		t.Fatal("restart lost current head", r)
	}
	withdrawal := harness.Review{Version: 1, ID: "withdrawal", ExecutionDigest: digest, ExpectedHead: revision.ID, Verdict: "withdrawn", Reviewer: "trusted-fixture", CreatedAt: time.Now().UTC()}
	if err := client.ReviewHarnessOutcome(ctx, ledger, "completed", withdrawal); err != nil {
		t.Fatal(err)
	}
	if r := ranked(); r.EffectiveSamples != 0 {
		t.Fatal("withdrawn vote still active", r)
	}
	if calls != 2 {
		t.Fatal("evidence operations executed inference", calls)
	}
}

func TestSDKHarnessEvidenceGuardsDoNotCreateJournal(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "missing.db")
	client, err := sdk.New(sdk.ConfigOptions{Overrides: map[string]string{"telemetry.database": path}})
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := harness.OpenEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	for _, c := range []*sdk.Client{nil, {}, client} {
		if _, err := c.ReconcileHarnessOutcome(ctx, ledger, "missing"); err == nil {
			t.Fatal("missing journal accepted")
		}
		if err := c.ReviewHarnessOutcome(ctx, ledger, "missing", harness.Review{}); err == nil {
			t.Fatal("invalid review accepted")
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("evidence operation created task journal", err)
	}
}
