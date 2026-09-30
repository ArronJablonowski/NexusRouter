// Command verify-published-install installs and executes the native archive
// from a verified GitHub download directory, then retains canonical evidence.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/releasepack"
)

type verifier func(context.Context, string, string, releasepack.PublishedInstallExpectations) (releasepack.PublishedInstallEvidence, error)

type evidenceOutput interface {
	Commit(releasepack.PublishedInstallEvidence) (string, error)
	Close() error
}

type preparer func(string, ...string) (evidenceOutput, error)

type options struct {
	receiptFile, receiptSHA, installFile, installSHA string
	backupSHA, targetOS, targetArch, verifierID      string
	downloadDir, installRoot, out                    string
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, now func() time.Time, verify verifier, prepare preparer) int {
	if ctx == nil || stdout == nil || stderr == nil || now == nil || verify == nil || prepare == nil {
		return 2
	}
	var o options
	set := flag.NewFlagSet("verify-published-install", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.StringVar(&o.receiptFile, "publication-receipt", "", "canonical post-publication receipt")
	set.StringVar(&o.receiptSHA, "publication-receipt-sha256", "", "independently supplied receipt digest")
	set.StringVar(&o.installFile, "install-evidence", "", "canonical native install/migration evidence")
	set.StringVar(&o.installSHA, "install-evidence-sha256", "", "independently supplied install-evidence digest")
	set.StringVar(&o.backupSHA, "backup-sha256", "", "independently supplied immutable backup digest")
	set.StringVar(&o.targetOS, "target-os", "", "actual native operating system")
	set.StringVar(&o.targetArch, "target-arch", "", "actual native architecture")
	set.StringVar(&o.verifierID, "verifier-id", "", "independent verifier identity")
	set.StringVar(&o.downloadDir, "download-dir", "", "freshly verified release download directory")
	set.StringVar(&o.installRoot, "install-root", "", "new private installation root")
	set.StringVar(&o.out, "out", "", "new canonical published-install evidence file")
	if err := set.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if set.NArg() != 0 || o.receiptFile == "" || o.receiptSHA == "" || o.installFile == "" || o.installSHA == "" ||
		o.backupSHA == "" || o.targetOS == "" || o.targetArch == "" || o.verifierID == "" || o.downloadDir == "" || o.installRoot == "" || o.out == "" {
		fmt.Fprintln(stderr, "all receipt, rehearsal, target, verifier, download, install, and output inputs are required")
		return 2
	}
	output, err := prepare(o.out, o.downloadDir, o.installRoot)
	if err != nil || output == nil {
		fmt.Fprintln(stderr, "published install evidence preflight failed")
		return 1
	}
	defer output.Close()
	expected := releasepack.PublishedInstallExpectations{
		PublicationReceiptSHA256: o.receiptSHA, InstallEvidenceSHA256: o.installSHA,
		TargetOS: o.targetOS, TargetArch: o.targetArch, BackupSHA256: o.backupSHA,
		VerifierID: o.verifierID, Now: func() time.Time { return now().UTC().Truncate(time.Second) },
		DownloadDir: o.downloadDir, InstallRoot: o.installRoot,
	}
	evidence, err := verify(ctx, o.receiptFile, o.installFile, expected)
	if err != nil {
		fmt.Fprintln(stderr, "published install verification failed")
		return 1
	}
	digest, err := output.Commit(evidence)
	if err != nil {
		fmt.Fprintln(stderr, "published install evidence persistence failed")
		return 1
	}
	if _, err = fmt.Fprintln(stdout, digest); err != nil {
		fmt.Fprintln(stderr, "published install result failed")
		return 1
	}
	return 0
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, time.Now,
		releasepack.CreatePublishedInstallEvidence,
		func(path string, roots ...string) (evidenceOutput, error) {
			return releasepack.PreparePublishedInstallEvidenceOutput(path, roots...)
		}))
}
