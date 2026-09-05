package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ArronJablonowski/DarwinRouter/internal/releasepack"
)

func main() {
	var o releasepack.Options
	flag.StringVar(&o.Version, "version", "", "explicit semantic release version")
	flag.StringVar(&o.Commit, "commit", "", "full lowercase source commit")
	flag.StringVar(&o.Out, "out", "", "new output directory")
	flag.StringVar(&o.Source, "source", ".", "clean source repository")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments")
		os.Exit(1)
	}
	if err := releasepack.Package(ctx, o); err != nil {
		fmt.Fprintln(os.Stderr, "release packaging failed:", err)
		os.Exit(1)
	}
}
