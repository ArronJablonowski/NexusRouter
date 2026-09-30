// Command license-evidence freezes or verifies candidate-bound dependency and
// license evidence. It never approves notices, signs, or publishes a release.
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

	"github.com/ArronJablonowski/NexusRouter/internal/releasepack"
)

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "required subcommand: freeze or verify")
		return 2
	}
	switch args[0] {
	case "freeze":
		flags := flag.NewFlagSet("license-evidence freeze", flag.ContinueOnError)
		flags.SetOutput(stderr)
		var options releasepack.LicenseEvidenceOptions
		flags.StringVar(&options.Commit, "commit", "", "full lowercase source commit")
		flags.StringVar(&options.Source, "source", ".", "clean source repository")
		flags.StringVar(&options.Out, "out", "", "new external evidence file")
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if options.Commit == "" || options.Out == "" || flags.NArg() != 0 {
			fmt.Fprintln(stderr, "required: freeze --commit FULL_COMMIT --out NEW_FILE [--source REPOSITORY]")
			return 2
		}
		digest, err := releasepack.FreezeLicenseEvidence(ctx, options)
		if err != nil {
			fmt.Fprintln(stderr, "license evidence freeze failed")
			return 1
		}
		fmt.Fprintln(stdout, digest)
		return 0
	case "verify":
		flags := flag.NewFlagSet("license-evidence verify", flag.ContinueOnError)
		flags.SetOutput(stderr)
		record := flags.String("record", "", "external canonical evidence record")
		digest := flags.String("record-sha256", "", "independently approved exact record digest")
		source := flags.String("source", ".", "clean source repository")
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if *record == "" || *digest == "" || flags.NArg() != 0 {
			fmt.Fprintln(stderr, "required: verify --record FILE --record-sha256 SHA256 [--source REPOSITORY]")
			return 2
		}
		if err := releasepack.VerifyLicenseEvidence(ctx, *record, *digest, *source); err != nil {
			fmt.Fprintln(stderr, "license evidence verification failed")
			return 1
		}
		return 0
	default:
		fmt.Fprintln(stderr, "unknown subcommand; required: freeze or verify")
		return 2
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
