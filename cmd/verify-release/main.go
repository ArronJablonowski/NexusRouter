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
	trustRecord := f.String("trust-record", "", "independently trusted canonical public trust-record file")
	keyID := f.String("key-id", "", "expected release-signing key ID (required with --trust-record)")
	fingerprint := f.String("key-fingerprint", "", "expected sha256 public-key fingerprint (required with --trust-record)")
	recordSHA256 := f.String("trust-record-sha256", "", "expected sha256 digest of exact canonical trust-record bytes")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	usingKey := *key != "" && *trustRecord == "" && *keyID == "" && *fingerprint == "" && *recordSHA256 == ""
	usingRecord := *key == "" && *trustRecord != "" && *keyID != "" && *fingerprint != "" && *recordSHA256 != ""
	if *dir == "" || (!usingKey && !usingRecord) || f.NArg() != 0 {
		fmt.Fprintln(stderr, "required: --dir DIRECTORY and either --public-key TRUSTED_KEY_FILE or --trust-record TRUSTED_RECORD --trust-record-sha256 EXPECTED_RECORD_SHA256 --key-id EXPECTED_KEY_ID --key-fingerprint EXPECTED_KEY_SHA256; no positional arguments")
		return 2
	}
	var err error
	if usingRecord {
		err = releasepack.VerifyTrustRecord(*dir, *trustRecord, *keyID, *fingerprint, *recordSHA256)
	} else {
		err = releasepack.Verify(*dir, *key)
	}
	if err != nil {
		fmt.Fprintln(stderr, "release verification failed")
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }
