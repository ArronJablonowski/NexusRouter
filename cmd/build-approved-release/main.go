// Command build-approved-release creates one retained unsigned release from
// two byte-identical builds of an exact externally frozen candidate.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/ArronJablonowski/DarwinRouter/internal/releasepack"
)

type approvedBuilder func(context.Context, releasepack.ApprovedBuildOptions) (releasepack.ApprovedBuildResult, error)

func run(ctx context.Context, args []string, stdout, stderr io.Writer, build approvedBuilder) int {
	flags := flag.NewFlagSet("build-approved-release", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var options releasepack.ApprovedBuildOptions
	flags.StringVar(&options.Out, "out", "", "new retained unsigned release directory")
	flags.StringVar(&options.Source, "source", ".", "clean source repository")
	flags.StringVar(&options.CandidateRecordFile, "candidate-record", "", "canonical external candidate record")
	flags.StringVar(&options.ExpectedCandidateSHA256, "candidate-record-sha256", "", "independently supplied sha256: digest")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || options.Out == "" || options.Source == "" ||
		options.CandidateRecordFile == "" || options.ExpectedCandidateSHA256 == "" || build == nil {
		fmt.Fprintln(stderr, "required: --out NEW_DIRECTORY --source REPOSITORY --candidate-record FILE --candidate-record-sha256 sha256:DIGEST")
		return 2
	}
	result, err := build(ctx, options)
	if err != nil {
		fmt.Fprintln(stderr, "approved release build failed")
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if err = encoder.Encode(result); err != nil {
		fmt.Fprintln(stderr, "approved release build result failed")
		return 1
	}
	return 0
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, releasepack.BuildApproved))
}
