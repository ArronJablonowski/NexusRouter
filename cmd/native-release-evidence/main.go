// Command native-release-evidence runs the release gates and writes one
// canonical native-target evidence record. It never signs or publishes.
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

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("native-release-evidence", flag.ContinueOnError)
	f.SetOutput(stderr)
	var options releasepack.Options
	f.StringVar(&options.Version, "version", "", "explicit semantic release version")
	f.StringVar(&options.Commit, "commit", "", "full lowercase source commit")
	f.StringVar(&options.Out, "out", "", "new external evidence file")
	f.StringVar(&options.Source, "source", ".", "clean source repository")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if options.Version == "" || options.Commit == "" || options.Out == "" || f.NArg() != 0 {
		fmt.Fprintln(stderr, "required: --version VERSION --commit FULL_COMMIT --out NEW_FILE [--source REPOSITORY]")
		return 2
	}
	if err := releasepack.QualifyNativeRelease(ctx, options, stdout); err != nil {
		fmt.Fprintln(stderr, "native release qualification failed")
		return 1
	}
	return 0
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
