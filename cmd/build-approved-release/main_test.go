package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/internal/releasepack"
)

func TestApprovedBuildArgumentsAndResult(t *testing.T) {
	var captured releasepack.ApprovedBuildOptions
	var stdout, stderr bytes.Buffer
	digest := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	code := run(context.Background(), []string{
		"--out", "/release", "--source", "/source", "--candidate-record", "/candidate.json",
		"--candidate-record-sha256", digest,
	}, &stdout, &stderr, func(_ context.Context, options releasepack.ApprovedBuildOptions) (releasepack.ApprovedBuildResult, error) {
		captured = options
		return releasepack.ApprovedBuildResult{CandidateRecordSHA256: digest, SHA256SUMSSHA256: digest}, nil
	})
	if code != 0 || stderr.Len() != 0 || captured.Out != "/release" || captured.Source != "/source" ||
		captured.CandidateRecordFile != "/candidate.json" || captured.ExpectedCandidateSHA256 != digest {
		t.Fatalf("arguments not forwarded: code=%d options=%+v stderr=%q", code, captured, stderr.String())
	}
	want := "{\"candidate_record_sha256\":\"" + digest + "\",\"sha256sums_sha256\":\"" + digest + "\"}\n"
	if stdout.String() != want {
		t.Fatalf("unexpected result: %q", stdout.String())
	}
}

func TestApprovedBuildCLIRejectsArgumentsAndFailure(t *testing.T) {
	for _, scenario := range []struct {
		name string
		args []string
		fail bool
		want int
	}{
		{name: "missing", want: 2},
		{name: "extra", args: []string{"--out", "o", "--source", "s", "--candidate-record", "c", "--candidate-record-sha256", "d", "extra"}, want: 2},
		{name: "build", args: []string{"--out", "o", "--source", "s", "--candidate-record", "c", "--candidate-record-sha256", "d"}, fail: true, want: 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			calls := 0
			code := run(context.Background(), scenario.args, &bytes.Buffer{}, &bytes.Buffer{}, func(context.Context, releasepack.ApprovedBuildOptions) (releasepack.ApprovedBuildResult, error) {
				calls++
				if scenario.fail {
					return releasepack.ApprovedBuildResult{}, errors.New("injected")
				}
				return releasepack.ApprovedBuildResult{}, nil
			})
			if code != scenario.want || calls != map[bool]int{true: 1, false: 0}[scenario.fail] {
				t.Fatalf("code=%d calls=%d", code, calls)
			}
		})
	}
}
