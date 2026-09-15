// Command release-candidate freezes or verifies an external release-candidate
// contract. It never approves, signs, tags, or publishes a release.
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

	"github.com/ArronJablonowski/DarwinRouter/internal/releasepack"
)

func run(ctx context.Context, args []string, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "required subcommand: freeze, verify, or notes")
		return 2
	}
	switch args[0] {
	case "freeze":
		flags := flag.NewFlagSet("release-candidate freeze", flag.ContinueOnError)
		flags.SetOutput(stderr)
		var options releasepack.Options
		flags.StringVar(&options.Version, "version", "", "explicit semantic release version")
		flags.StringVar(&options.Commit, "commit", "", "full lowercase source commit")
		flags.StringVar(&options.Out, "out", "", "new candidate-record file")
		flags.StringVar(&options.Source, "source", ".", "clean source repository")
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if options.Version == "" || options.Commit == "" || options.Out == "" || flags.NArg() != 0 {
			fmt.Fprintln(stderr, "required: freeze --version VERSION --commit FULL_COMMIT --out NEW_FILE [--source REPOSITORY]")
			return 2
		}
		if err := releasepack.FreezeCandidate(ctx, options); err != nil {
			fmt.Fprintln(stderr, "candidate freeze failed")
			return 1
		}
		return 0
	case "verify":
		flags := flag.NewFlagSet("release-candidate verify", flag.ContinueOnError)
		flags.SetOutput(stderr)
		record := flags.String("record", "", "candidate-record file")
		source := flags.String("source", ".", "clean source repository")
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if *record == "" || flags.NArg() != 0 {
			fmt.Fprintln(stderr, "required: verify --record FILE [--source REPOSITORY]")
			return 2
		}
		if err := releasepack.VerifyCandidate(ctx, *record, *source); err != nil {
			fmt.Fprintln(stderr, "candidate verification failed")
			return 1
		}
		return 0
	case "notes":
		flags := flag.NewFlagSet("release-candidate notes", flag.ContinueOnError)
		flags.SetOutput(stderr)
		record := flags.String("record", "", "candidate-record file")
		source := flags.String("source", ".", "clean source repository")
		out := flags.String("out", "", "new final release-notes file")
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if *record == "" || *out == "" || flags.NArg() != 0 {
			fmt.Fprintln(stderr, "required: notes --record FILE --out NEW_FILE [--source REPOSITORY]")
			return 2
		}
		if err := releasepack.WriteFinalReleaseNotes(ctx, *record, *source, *out); err != nil {
			fmt.Fprintln(stderr, "candidate release-notes generation failed")
			return 1
		}
		return 0
	default:
		fmt.Fprintln(stderr, "unknown subcommand; required: freeze, verify, or notes")
		return 2
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stderr))
}
