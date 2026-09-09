package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/releasepack"
)

var errUsage = errors.New("invalid rollback-readiness arguments")

type flags struct {
	record, recordSHA, receipt, receiptSHA, authorizationSHA, receiptVerifier string
	repository, version, commit, tag, mode                                    string
	rehearsal, rehearsalSHA, rehearsalVerifier                                string
	backup, backupSHA, backupVerifier                                         string
	priorVersion, priorCommit, priorTag, priorOS, priorArch                   string
	priorArtifact, priorArtifactSHA, priorBinarySHA, priorReceiptSHA          string
	priorVerificationSHA                                                      string
	owner, statusURL, approver, policyURL                                     string
	readinessVerifier, out                                                    string
	firstDaemonAction, firstBinaryAction, firstDataAction                     string
	releaseID, currentSchema, backupSchema, priorSchema                       int64
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, time.Now, releasepack.VerifyRollbackReadiness); err != nil {
		fmt.Fprintln(os.Stderr, "rollback readiness verification failed")
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, now func() time.Time, verify func(context.Context, releasepack.RollbackReadinessOptions) (releasepack.RollbackReadinessResult, error)) error {
	if ctx == nil || stdout == nil || stderr == nil || now == nil || verify == nil {
		return errUsage
	}
	var f flags
	set := flag.NewFlagSet("verify-rollback-readiness", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.StringVar(&f.record, "record", "", "canonical rollback-readiness record")
	set.StringVar(&f.recordSHA, "record-sha256", "", "independently expected readiness-record digest")
	set.StringVar(&f.receipt, "publication-receipt", "", "canonical post-publication verification receipt")
	set.StringVar(&f.receiptSHA, "publication-receipt-sha256", "", "independently expected publication-receipt digest")
	set.StringVar(&f.authorizationSHA, "publication-authorization-sha256", "", "expected publication authorization digest")
	set.StringVar(&f.receiptVerifier, "receipt-verifier-id", "", "independent receipt observation identity")
	set.StringVar(&f.repository, "repository", "", "expected owner/repository")
	set.StringVar(&f.version, "version", "", "expected release version")
	set.StringVar(&f.commit, "commit", "", "expected full source commit")
	set.StringVar(&f.tag, "tag", "", "expected release tag")
	set.Int64Var(&f.releaseID, "release-id", 0, "expected immutable release ID")
	set.Int64Var(&f.currentSchema, "current-schema", 0, "current durable-state schema")
	set.StringVar(&f.mode, "mode", "", "explicit first_release or upgrade policy")
	set.StringVar(&f.rehearsal, "rehearsal-evidence", "", "canonical published-install evidence")
	set.StringVar(&f.rehearsalSHA, "rehearsal-sha256", "", "expected rehearsal evidence digest")
	set.StringVar(&f.rehearsalVerifier, "rehearsal-verifier-id", "", "independent rehearsal verifier")
	set.StringVar(&f.backup, "backup", "", "pre-upgrade backup (upgrade only)")
	set.StringVar(&f.backupSHA, "backup-sha256", "", "expected backup digest")
	set.Int64Var(&f.backupSchema, "backup-schema", 0, "backup schema")
	set.StringVar(&f.backupVerifier, "backup-verifier-id", "", "backup verifier identity")
	set.StringVar(&f.priorVersion, "prior-version", "", "prior supported release version")
	set.StringVar(&f.priorCommit, "prior-commit", "", "prior supported full source commit")
	set.StringVar(&f.priorTag, "prior-tag", "", "prior supported release tag")
	set.StringVar(&f.priorOS, "prior-target-os", "", "prior binary operating system")
	set.StringVar(&f.priorArch, "prior-target-arch", "", "prior binary architecture")
	set.StringVar(&f.priorArtifact, "prior-artifact-name", "", "prior release artifact name")
	set.StringVar(&f.priorArtifactSHA, "prior-artifact-sha256", "", "prior artifact digest")
	set.StringVar(&f.priorBinarySHA, "prior-binary-sha256", "", "prior binary digest")
	set.StringVar(&f.priorReceiptSHA, "prior-publication-receipt-sha256", "", "prior publication receipt digest")
	set.StringVar(&f.priorVerificationSHA, "prior-verification-receipt-sha256", "", "prior binary verification receipt digest")
	set.Int64Var(&f.priorSchema, "prior-schema", 0, "prior durable-state schema")
	set.StringVar(&f.owner, "incident-owner", "", "incident owner identity")
	set.StringVar(&f.statusURL, "status-url", "", "HTTPS incident status channel")
	set.StringVar(&f.approver, "approver-id", "", "readiness approver identity")
	set.StringVar(&f.policyURL, "policy-url", "", "HTTPS approval policy")
	set.StringVar(&f.readinessVerifier, "readiness-verifier-id", "", "independent rollback-readiness verifier")
	set.StringVar(&f.out, "out", "", "new canonical rollback-readiness verification receipt")
	set.StringVar(&f.firstDaemonAction, "first-release-daemon-action", "", "approved first-release daemon action")
	set.StringVar(&f.firstBinaryAction, "first-release-binary-action", "", "approved first-release binary action")
	set.StringVar(&f.firstDataAction, "first-release-data-action", "", "approved first-release data action")
	if err := set.Parse(args); err != nil || set.NArg() != 0 || (f.mode != "first_release" && f.mode != "upgrade") {
		return errUsage
	}
	priorUsed := f.priorVersion != "" || f.priorCommit != "" || f.priorTag != "" || f.priorOS != "" || f.priorArch != "" || f.priorArtifact != "" || f.priorArtifactSHA != "" || f.priorBinarySHA != "" || f.priorReceiptSHA != "" || f.priorVerificationSHA != "" || f.priorSchema != 0
	backupUsed := f.backup != "" || f.backupSHA != "" || f.backupSchema != 0 || f.backupVerifier != ""
	firstPolicyUsed := f.firstDaemonAction != "" || f.firstBinaryAction != "" || f.firstDataAction != ""
	if f.out == "" || f.readinessVerifier == "" || f.mode == "first_release" && (priorUsed || backupUsed || !firstPolicyUsed) || f.mode == "upgrade" && (!priorUsed || !backupUsed || firstPolicyUsed) {
		return errUsage
	}
	output, err := releasepack.PrepareRollbackEvidenceOutput(f.out)
	if err != nil {
		return err
	}
	defer output.Close()
	options := releasepack.RollbackReadinessOptions{
		RecordFile: f.record, ReceiptFile: f.receipt, RehearsalFile: f.rehearsal,
		ReceiptVerifier: releasepack.CanonicalPostPublicationReceiptVerifier{VerifierID: f.receiptVerifier},
		Expectations: releasepack.RollbackReadinessExpectations{
			RecordSHA256: f.recordSHA, PublicationReceiptSHA256: f.receiptSHA,
			PublicationAuthorizationSHA256: f.authorizationSHA, ReceiptVerifierID: f.receiptVerifier,
			Repository: f.repository, ReleaseVersion: f.version, SourceCommit: f.commit,
			Tag: f.tag, ReleaseID: f.releaseID, CurrentStateSchema: int(f.currentSchema), Mode: f.mode,
			RehearsalSHA256: f.rehearsalSHA, RehearsalVerifierID: f.rehearsalVerifier,
			IncidentOwnerID: f.owner, StatusURL: f.statusURL, ApproverID: f.approver, PolicyURL: f.policyURL,
			FirstReleaseDaemonAction: f.firstDaemonAction, FirstReleaseBinaryAction: f.firstBinaryAction, FirstReleaseDataAction: f.firstDataAction,
		},
		RehearsalVerifier:   releasepack.CanonicalRollbackRehearsalVerifier{VerifierID: f.rehearsalVerifier},
		ReadinessVerifierID: f.readinessVerifier,
		Now:                 func() time.Time { return now().UTC().Truncate(time.Second) },
	}
	if f.mode == "upgrade" {
		options.BackupFile = f.backup
		options.Expectations.BackupSHA256 = f.backupSHA
		options.Expectations.BackupStateSchema = int(f.backupSchema)
		options.Expectations.BackupVerifierID = f.backupVerifier
		options.Expectations.ExpectedPrior = &releasepack.RollbackPriorSupportedBinary{
			Repository: f.repository, ReleaseVersion: f.priorVersion, SourceCommit: f.priorCommit,
			Tag: f.priorTag, TargetOS: f.priorOS, TargetArch: f.priorArch,
			ArtifactName: f.priorArtifact, ArtifactSHA256: f.priorArtifactSHA,
			BinarySHA256: f.priorBinarySHA, PublicationReceiptSHA256: f.priorReceiptSHA,
			VerificationReceiptSHA256: f.priorVerificationSHA, StateSchema: int(f.priorSchema),
		}
	}
	result, err := verify(ctx, options)
	if err != nil {
		return err
	}
	body, err := releasepack.MarshalRollbackReadinessResult(result)
	if err != nil {
		return err
	}
	digest, err := output.CommitCanonical(body)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, digest)
	return err
}
