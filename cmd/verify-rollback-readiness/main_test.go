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
	args := baseArgs(t)
	var got releasepack.RollbackReadinessOptions
	verify := func(_ context.Context, options releasepack.RollbackReadinessOptions) (releasepack.RollbackReadinessResult, error) {
		got = options
		return validCLIResult("first_release"), nil
	}
	var stdout bytes.Buffer
	when := time.Date(2026, 9, 7, 19, 0, 0, 0, time.FixedZone("offset", -6*60*60))
	if err := run(t.Context(), args, &stdout, &bytes.Buffer{}, func() time.Time { return when }, verify); err != nil {
		t.Fatal(err)
	}
	if got.Expectations.Mode != "first_release" || got.Expectations.ReceiptVerifierID != "idp:receipt" || got.Expectations.ExpectedPrior != nil || !got.Now().Equal(when.UTC().Truncate(time.Second)) || got.ReadinessVerifierID != "idp:readiness" || !strings.HasPrefix(stdout.String(), "sha256:") {
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
		"unknown_mode":          append(baseArgs(t), "--mode", "automatic"),
		"first_with_backup":     append(baseArgs(t), "--backup=backup.db"),
		"upgrade_without_prior": append(baseArgs(t), "--mode", "upgrade"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(t.Context(), args, &bytes.Buffer{}, &bytes.Buffer{}, time.Now, verify); err == nil {
				t.Fatal("unsafe or incomplete policy accepted")
			}
		})
	}
}

func baseArgs(t *testing.T) []string {
	t.Helper()
	args := strings.Fields("--record readiness.json --record-sha256 sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa --publication-receipt receipt.json --publication-receipt-sha256 sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb --publication-authorization-sha256 sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc --receipt-verifier-id idp:receipt --repository ArronJablonowski/DarwinRouter --version 1.0.0 --commit aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa --tag v1.0.0 --release-id 1 --current-schema 32 --mode first_release --rehearsal-evidence rehearsal.log --rehearsal-sha256 sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd --rehearsal-verifier-id idp:rehearsal --incident-owner team:incident --status-url https://status.example.invalid --approver-id idp:approver --policy-url https://policy.example.invalid --readiness-verifier-id idp:readiness --first-release-daemon-action stop --first-release-binary-action uninstall --first-release-data-action preserve_current_schema_no_restore")
	return append(args, "--out", t.TempDir()+"/verification.json")
}

func validCLIResult(mode string) releasepack.RollbackReadinessResult {
	return releasepack.RollbackReadinessResult{SchemaVersion: 1, Scope: "darwinrouter-rollback-readiness-verification", RecordSHA256: "sha256:" + strings.Repeat("a", 64), PublicationReceiptSHA256: "sha256:" + strings.Repeat("b", 64), RehearsalSHA256: "sha256:" + strings.Repeat("d", 64), Repository: "ArronJablonowski/DarwinRouter", ReleaseVersion: "1.0.0", SourceCommit: strings.Repeat("a", 40), Tag: "v1.0.0", ReleaseID: 1, Mode: mode, StateSchema: 32, ValidUntil: "2026-09-09T00:00:00Z", ReadinessVerifierID: "idp:readiness", VerifiedAt: "2026-09-08T01:00:00Z"}
}
