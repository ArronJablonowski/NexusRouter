// Command verify-release checks signatures and all release payloads without
// extracting or executing them. Public-key trust is always explicit.
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
	f := flag.NewFlagSet("verify-release", flag.ContinueOnError)
	f.SetOutput(stderr)
	dir := f.String("dir", "", "signed release directory")
	key := f.String("public-key", "", "independently trusted Ed25519 public-key file (hex)")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *dir == "" || *key == "" || f.NArg() != 0 {
		fmt.Fprintln(stderr, "required: --dir DIRECTORY --public-key TRUSTED_KEY_FILE; no positional arguments")
		return 2
	}
	if err := releasepack.Verify(*dir, *key); err != nil {
		fmt.Fprintln(stderr, "release verification failed")
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }
