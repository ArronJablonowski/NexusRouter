package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/releasepack"
)

func TestRunPassesExplicitFirstReleaseExpectations(t *testing.T) {
	args := baseArgs()
	var got releasepack.RollbackReadinessOptions
	verify := func(_ context.Context, options releasepack.RollbackReadinessOptions) (releasepack.RollbackReadinessResult, error) {
		got = options
		return releasepack.RollbackReadinessResult{Mode: "first_release"}, nil
	}
	var stdout bytes.Buffer
	when := time.Date(2026, 9, 7, 19, 0, 0, 0, time.FixedZone("offset", -6*60*60))
	if err := run(t.Context(), args, &stdout, &bytes.Buffer{}, func() time.Time { return when }, verify); err != nil {
		t.Fatal(err)
	}
	if got.Expectations.Mode != "first_release" || got.Expectations.ReceiptVerifierID != "idp:receipt" || got.Expectations.ExpectedPrior != nil || !got.VerificationTime.Equal(when) || got.VerificationTime.Location() != time.UTC || stdout.String() != "{\"record_sha256\":\"\",\"publication_receipt_sha256\":\"\",\"repository\":\"\",\"release_version\":\"\",\"source_commit\":\"\",\"tag\":\"\",\"release_id\":0,\"mode\":\"first_release\",\"state_schema\":0,\"valid_until\":\"\"}\n" {
		t.Fatal("CLI did not preserve explicit inputs", got, stdout.String())
	}
}

func TestRunRejectsPolicyInferenceAndMixedEvidence(t *testing.T) {
	verify := func(context.Context, releasepack.RollbackReadinessOptions) (releasepack.RollbackReadinessResult, error) {
		t.Fatal("verifier called for invalid arguments")
		return releasepack.RollbackReadinessResult{}, nil
	}
	for name, args := range map[string][]string{
		"missing_mode":          {"--record", "record.json"},
		"unknown_mode":          append(baseArgs(), "--mode", "automatic"),
		"first_with_backup":     append(baseArgs(), "--backup=backup.db"),
		"upgrade_without_prior": append(baseArgs(), "--mode", "upgrade"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(t.Context(), args, &bytes.Buffer{}, &bytes.Buffer{}, time.Now, verify); err == nil {
				t.Fatal("unsafe or incomplete policy accepted")
			}
		})
	}
}

func baseArgs() []string {
	return strings.Fields("--record readiness.json --record-sha256 sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa --publication-receipt receipt.json --publication-receipt-sha256 sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb --publication-authorization-sha256 sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc --receipt-verifier-id idp:receipt --repository ArronJablonowski/DarwinRouter --version 1.0.0 --commit aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa --tag v1.0.0 --release-id 1 --current-schema 1 --mode first_release --rehearsal-evidence rehearsal.log --rehearsal-sha256 sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd --rehearsal-verifier-id idp:rehearsal --incident-owner team:incident --status-url https://status.example.invalid --approver-id idp:approver --policy-url https://policy.example.invalid")
}
