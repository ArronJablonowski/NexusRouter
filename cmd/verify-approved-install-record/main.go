// Command verify-approved-install-record validates a retained canonical native
// install receipt against an independently supplied digest.
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

type verifier func(string, string) (releasepack.ApprovedInstallVerificationReceipt, error)

func run(args []string, stdout, stderr io.Writer, verify verifier) int {
	if stdout == nil || stderr == nil || verify == nil {
		return 2
	}
	set := flag.NewFlagSet("verify-approved-install-record", flag.ContinueOnError)
	set.SetOutput(stderr)
	var record, digest string
	set.StringVar(&record, "record", "", "canonical approved native install verification receipt")
	set.StringVar(&digest, "record-sha256", "", "independently supplied receipt digest")
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
	receipt, err := verify(record, digest)
	if err != nil {
		fmt.Fprintln(stderr, "approved install verification receipt failed")
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(receipt) != nil {
		fmt.Fprintln(stderr, "approved install verification receipt result failed")
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, releasepack.VerifyApprovedInstallVerificationReceipt))
}
