// Command sign-release signs a local release using an explicitly supplied
// release-only Ed25519 seed. It never discovers credentials or uploads artifacts.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ArronJablonowski/DarwinRouter/internal/releasepack"
)

func run(args []string, stderr io.Writer) int {
	f := flag.NewFlagSet("sign-release", flag.ContinueOnError)
	f.SetOutput(stderr)
	dir := f.String("dir", "", "existing unsigned release directory")
	key := f.String("key", "", "release-only Ed25519 seed file (hex, mode 0400 or 0600)")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *dir == "" || *key == "" || f.NArg() != 0 {
		fmt.Fprintln(stderr, "required: --dir DIRECTORY --key RELEASE_SEED_FILE; no positional arguments")
		return 2
	}
	if err := releasepack.Sign(*dir, *key); err != nil {
		fmt.Fprintln(stderr, "release signing failed")
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }
