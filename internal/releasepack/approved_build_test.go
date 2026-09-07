package releasepack

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApprovedBuildRetainsOneOfExactlyTwoComparedBuilds(t *testing.T) {
	signing, _ := approvedSigningFixture(t)
	out := filepath.Join(t.TempDir(), "release")
	options := ApprovedBuildOptions{
		Out: out, Source: signing.Source, CandidateRecordFile: signing.CandidateRecordFile,
		ExpectedCandidateSHA256: signing.ExpectedCandidateSHA256,
	}
	_, candidate, err := readCandidateRecord(signing.CandidateRecordFile)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	var buildOutputs []string
	result, err := buildApproved(context.Background(), options, func(_ context.Context, build Options) error {
		calls++
		buildOutputs = append(buildOutputs, build.Out)
		if build.Version != candidate.ReleaseVersion || build.Commit != candidate.SourceCommit || build.Source != signing.Source {
			t.Fatal("builder received unbound identity", build)
		}
		return copyUnsignedRelease(signing.Dir, build.Out)
	})
	if err != nil || calls != 2 || buildOutputs[0] == buildOutputs[1] || buildOutputs[0] == out || buildOutputs[1] == out {
		t.Fatalf("build failed or wrong count: calls=%d err=%v", calls, err)
	}
	root, sums, err := checkedRelease(out, false)
	if err != nil {
		t.Fatal(err)
	}
	root.Close()
	if result.CandidateRecordSHA256 != signing.ExpectedCandidateSHA256 || result.SHA256SUMSSHA256 != prefixedDigest(sums) {
		t.Fatal("wrong retained identity", result)
	}
	for _, name := range releaseFixtureNames(t, signing.Dir) {
		want, readErr := os.ReadFile(filepath.Join(signing.Dir, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		got, readErr := os.ReadFile(filepath.Join(out, name))
		if readErr != nil || string(got) != string(want) {
			t.Fatalf("retained output was reconstructed: %s: %v", name, readErr)
		}
	}
}

func TestApprovedBuildRejectsBeforeOrWithoutRetainingOutput(t *testing.T) {
	for _, scenario := range []string{"candidate_digest", "existing_output", "second_build", "non_reproducible", "source_changed", "candidate_changed", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			signing, _ := approvedSigningFixture(t)
			parent := t.TempDir()
			out := filepath.Join(parent, "release")
			options := ApprovedBuildOptions{
				Out: out, Source: signing.Source, CandidateRecordFile: signing.CandidateRecordFile,
				ExpectedCandidateSHA256: signing.ExpectedCandidateSHA256,
			}
			ctx := context.Background()
			if scenario == "candidate_digest" {
				options.ExpectedCandidateSHA256 = "sha256:" + string(make([]byte, 64))
			}
			if scenario == "existing_output" {
				if err := os.Mkdir(out, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			calls := 0
			_, err := buildApproved(ctx, options, func(_ context.Context, build Options) error {
				calls++
				if scenario == "second_build" && calls == 2 {
					return errors.New("injected")
				}
				if err := copyUnsignedRelease(signing.Dir, build.Out); err != nil {
					return err
				}
				switch scenario {
				case "non_reproducible":
					if calls == 2 {
						path := filepath.Join(build.Out, "manifest.json")
						body, readErr := os.ReadFile(path)
						if readErr != nil {
							return readErr
						}
						var manifest Manifest
						if json.Unmarshal(body, &manifest) != nil {
							return ErrInvalid
						}
						manifest.Toolchain = "go1.27.2"
						body, readErr = json.MarshalIndent(manifest, "", "  ")
						if readErr != nil {
							return readErr
						}
						if readErr = os.WriteFile(path, append(body, '\n'), 0644); readErr != nil {
							return readErr
						}
						refreshSigningFixture(t, build.Out)
					}
				case "source_changed":
					if calls == 2 {
						return os.WriteFile(filepath.Join(signing.Source, "dirty"), []byte("dirty\n"), 0600)
					}
				case "candidate_changed":
					if calls == 2 {
						return os.WriteFile(signing.CandidateRecordFile, []byte("{}\n"), 0644)
					}
				}
				return nil
			})
			if err == nil {
				t.Fatal("unsafe build accepted")
			}
			if scenario == "existing_output" {
				if calls != 0 {
					t.Fatal("builder ran for existing output")
				}
				return
			}
			if _, statErr := os.Lstat(out); !os.IsNotExist(statErr) {
				t.Fatalf("failed build retained output: %v", statErr)
			}
			if (scenario == "candidate_digest" || scenario == "canceled") && calls != 0 {
				t.Fatal("builder ran before public preflight")
			}
		})
	}
}

func copyUnsignedRelease(source, destination string) error {
	if err := os.Mkdir(destination, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		body, readErr := os.ReadFile(filepath.Join(source, entry.Name()))
		if readErr != nil {
			return readErr
		}
		if writeErr := os.WriteFile(filepath.Join(destination, entry.Name()), body, 0644); writeErr != nil {
			return writeErr
		}
	}
	return nil
}

func releaseFixtureNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
