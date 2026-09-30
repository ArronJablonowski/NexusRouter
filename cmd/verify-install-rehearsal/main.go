// Command verify-install-rehearsal validates a canonical, path-free record
// retained by NexusRouter's actual native archive installation rehearsal.
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

var errUsage = errors.New("invalid install-rehearsal verification arguments")

type flags struct {
	record, recordSHA, version, commit, targetOS, targetArch string
	artifact, artifactSHA, backupSHA                         string
	sourceSchema, currentSchema                              int
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, releasepack.VerifyInstallRehearsalEvidence))
}

func run(args []string, stdout, stderr io.Writer, verify func(string, releasepack.InstallRehearsalExpectations) (releasepack.InstallRehearsalVerification, error)) int {
	if stdout == nil || stderr == nil || verify == nil {
		return 2
	}
	var f flags
	set := flag.NewFlagSet("verify-install-rehearsal", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.StringVar(&f.record, "record", "", "canonical retained rehearsal record")
	set.StringVar(&f.recordSHA, "record-sha256", "", "independently expected record digest")
	set.StringVar(&f.version, "version", "", "expected release version")
	set.StringVar(&f.commit, "commit", "", "expected full source commit")
	set.StringVar(&f.targetOS, "target-os", "", "expected native operating system")
	set.StringVar(&f.targetArch, "target-arch", "", "expected native architecture")
	set.StringVar(&f.artifact, "artifact", "", "expected archive name")
	set.StringVar(&f.artifactSHA, "artifact-sha256", "", "expected archive digest")
	set.IntVar(&f.sourceSchema, "source-schema", 0, "expected pre-upgrade schema")
	set.IntVar(&f.currentSchema, "current-schema", 0, "expected migrated schema")
	set.StringVar(&f.backupSHA, "backup-sha256", "", "expected immutable backup digest")
	if err := set.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	expected := releasepack.InstallRehearsalExpectations{
		RecordSHA256: f.recordSHA, Version: f.version, Commit: f.commit,
		TargetOS: f.targetOS, TargetArch: f.targetArch, ArtifactName: f.artifact,
		ArtifactSHA256: f.artifactSHA, SourceSchema: f.sourceSchema,
		CurrentSchema: f.currentSchema, BackupSHA256: f.backupSHA,
	}
	if set.NArg() != 0 || f.record == "" || expected.Validate() != nil {
		fmt.Fprintln(stderr, errUsage)
		return 2
	}
	result, err := verify(f.record, expected)
	if err != nil {
		fmt.Fprintln(stderr, "install rehearsal evidence verification failed")
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(result) != nil {
		fmt.Fprintln(stderr, "install rehearsal evidence verification failed")
		return 1
	}
	return 0
}
