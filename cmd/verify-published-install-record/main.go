// Command verify-published-install-record validates retained canonical native
// execution evidence against an independently supplied record digest.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ArronJablonowski/NexusRouter/internal/releasepack"
)

type verifier func(string, string) (releasepack.PublishedInstallEvidence, error)

func run(args []string, stdout, stderr io.Writer, verify verifier) int {
	if stdout == nil || stderr == nil || verify == nil {
		return 2
	}
	set := flag.NewFlagSet("verify-published-install-record", flag.ContinueOnError)
	set.SetOutput(stderr)
	var record, digest string
	set.StringVar(&record, "record", "", "canonical published-native install evidence")
	set.StringVar(&digest, "record-sha256", "", "independently supplied record digest")
	if err := set.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if set.NArg() != 0 || record == "" || digest == "" {
		fmt.Fprintln(stderr, "record and independently supplied record-sha256 are required")
		return 2
	}
	evidence, err := verify(record, digest)
	if err != nil {
		fmt.Fprintln(stderr, "published install record verification failed")
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(evidence) != nil {
		fmt.Fprintln(stderr, "published install record result failed")
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, releasepack.VerifyPublishedInstallReceipt))
}
