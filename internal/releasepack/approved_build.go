package releasepack

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrApprovedBuild deliberately omits paths and untrusted build output.
var ErrApprovedBuild = errors.New("approved release build failed")

// ApprovedBuildOptions binds a retained unsigned release directory to an exact
// externally reviewed candidate record and clean source checkout.
type ApprovedBuildOptions struct {
	Out                     string
	Source                  string
	CandidateRecordFile     string
	ExpectedCandidateSHA256 string
}

// ApprovedBuildResult identifies the exact retained unsigned artifact set.
// Digests use "sha256:" followed by 64 lowercase hexadecimal characters.
type ApprovedBuildResult struct {
	CandidateRecordSHA256 string `json:"candidate_record_sha256"`
	SHA256SUMSSHA256      string `json:"sha256sums_sha256"`
}

// BuildApproved builds the candidate twice in separate private directories,
// compares every unsigned byte, and exclusively retains the first compared
// output. It does not approve, sign, tag, upload, or publish a release.
func BuildApproved(ctx context.Context, options ApprovedBuildOptions) (ApprovedBuildResult, error) {
	return buildApproved(ctx, options, Package)
}

type packageBuilder func(context.Context, Options) error

func buildApproved(ctx context.Context, options ApprovedBuildOptions, build packageBuilder) (ApprovedBuildResult, error) {
	var result ApprovedBuildResult
	if ctx == nil || build == nil || options.Out == "" || options.Source == "" ||
		options.CandidateRecordFile == "" || !trustFingerprint(options.ExpectedCandidateSHA256) {
		return result, ErrApprovedBuild
	}
	if ctx.Err() != nil {
		return result, ErrApprovedBuild
	}
	candidateBody, candidate, err := readCandidateRecord(options.CandidateRecordFile)
	if err != nil || prefixedDigest(candidateBody) != options.ExpectedCandidateSHA256 ||
		verifyCandidateRecord(ctx, candidate, options.Source) != nil {
		return result, ErrApprovedBuild
	}
	source, err := filepath.Abs(options.Source)
	if err != nil {
		return result, ErrApprovedBuild
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return result, ErrApprovedBuild
	}
	out, err := filepath.Abs(options.Out)
	if err != nil {
		return result, ErrApprovedBuild
	}
	outputRoot, outputName, err := candidateOutputRoot(out, source)
	if err != nil {
		return result, ErrApprovedBuild
	}
	defer outputRoot.Close()
	lockName := outputName + ".approved-build.lock"
	lock, err := outputRoot.OpenFile(lockName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, ErrApprovedBuild
	}
	if err = lock.Close(); err != nil {
		_ = outputRoot.Remove(lockName)
		return result, ErrApprovedBuild
	}
	defer outputRoot.Remove(lockName)

	parent, err := filepath.EvalSymlinks(filepath.Dir(out))
	if err != nil {
		return result, ErrApprovedBuild
	}
	parentHandle, err := os.Open(parent)
	if err != nil {
		return result, ErrApprovedBuild
	}
	defer parentHandle.Close()
	pinnedInfo, err := parentHandle.Stat()
	rootInfo, rootErr := outputRoot.Stat(".")
	if err != nil || rootErr != nil || !os.SameFile(pinnedInfo, rootInfo) {
		return result, ErrApprovedBuild
	}
	work, err := os.MkdirTemp(parent, ".darwin-approved-build-")
	if err != nil {
		return result, ErrApprovedBuild
	}
	defer os.RemoveAll(work)
	first, second := filepath.Join(work, "first"), filepath.Join(work, "second")
	buildOptions := Options{Version: candidate.ReleaseVersion, Commit: candidate.SourceCommit, Source: source}
	buildOptions.Out = first
	if err = build(ctx, buildOptions); err != nil {
		return result, ErrApprovedBuild
	}
	buildOptions.Out = second
	if err = build(ctx, buildOptions); err != nil {
		return result, ErrApprovedBuild
	}
	sums, err := compareCandidateBuilds(first, second, candidate)
	if err != nil {
		return result, ErrApprovedBuild
	}
	finalBody, _, err := readCandidateRecord(options.CandidateRecordFile)
	if err != nil || !bytes.Equal(finalBody, candidateBody) ||
		verifyCandidateCheckout(ctx, source, candidate.SourceCommit, environment()) != nil || ctx.Err() != nil {
		return result, ErrApprovedBuild
	}
	if _, err = outputRoot.Lstat(outputName); !os.IsNotExist(err) {
		return result, ErrApprovedBuild
	}
	// publish is an OS-level no-replace rename. The retained directory is the
	// first compared build itself, never an unverified third reconstruction.
	firstRelative, err := filepath.Rel(parent, first)
	if err != nil || firstRelative == "." || firstRelative == ".." ||
		strings.HasPrefix(firstRelative, ".."+string(filepath.Separator)) || filepath.IsAbs(firstRelative) {
		return result, ErrApprovedBuild
	}
	retained := filepath.Join(parent, outputName)
	if err = publishWithin(parentHandle, firstRelative, outputName); err != nil {
		return result, ErrApprovedBuild
	}
	if err = parentHandle.Sync(); err != nil {
		return result, ErrApprovedBuild
	}
	root, retainedSums, err := checkedRelease(retained, false)
	if err != nil || !bytes.Equal(retainedSums, sums) || approvedArtifactIdentity(root, candidate) != nil {
		if root != nil {
			root.Close()
		}
		return result, ErrApprovedBuild
	}
	if root.Close() != nil || verifyCandidateCheckout(ctx, source, candidate.SourceCommit, environment()) != nil {
		return result, ErrApprovedBuild
	}
	result.CandidateRecordSHA256 = options.ExpectedCandidateSHA256
	result.SHA256SUMSSHA256 = prefixedDigest(retainedSums)
	return result, nil
}

func compareCandidateBuilds(first, second string, candidate CandidateRecord) ([]byte, error) {
	firstRoot, firstSums, err := checkedRelease(first, false)
	if err != nil {
		return nil, ErrApprovedBuild
	}
	defer firstRoot.Close()
	secondRoot, secondSums, err := checkedRelease(second, false)
	if err != nil {
		return nil, ErrApprovedBuild
	}
	defer secondRoot.Close()
	if !bytes.Equal(firstSums, secondSums) || approvedArtifactIdentity(firstRoot, candidate) != nil ||
		approvedArtifactIdentity(secondRoot, candidate) != nil {
		return nil, ErrApprovedBuild
	}
	names := make([]string, 0, len(candidate.Targets)+2)
	for _, target := range candidate.Targets {
		names = append(names, "DarwinRouter_"+candidate.ReleaseVersion+"_"+target.OS+"_"+target.Arch+".tar.gz")
	}
	names = append(names, "manifest.json", "SHA256SUMS")
	for _, name := range names {
		if equal, err := equalReleaseFile(firstRoot, secondRoot, name); err != nil || !equal {
			return nil, ErrApprovedBuild
		}
	}
	return append([]byte(nil), firstSums...), nil
}

func equalReleaseFile(firstRoot, secondRoot *os.Root, name string) (bool, error) {
	first, err := openReleaseFile(firstRoot, name)
	if err != nil {
		return false, err
	}
	defer first.Close()
	second, err := openReleaseFile(secondRoot, name)
	if err != nil {
		return false, err
	}
	defer second.Close()
	firstInfo, err := first.Stat()
	if err != nil {
		return false, err
	}
	secondInfo, err := second.Stat()
	if err != nil || firstInfo.Size() != secondInfo.Size() {
		return false, err
	}
	left, right := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		leftN, leftErr := io.ReadFull(first, left)
		rightN, rightErr := io.ReadFull(second, right)
		if leftN != rightN || !bytes.Equal(left[:leftN], right[:rightN]) {
			return false, nil
		}
		if errors.Is(leftErr, io.ErrUnexpectedEOF) && errors.Is(rightErr, io.ErrUnexpectedEOF) {
			return true, nil
		}
		if leftErr == io.EOF && rightErr == io.EOF {
			return true, nil
		}
		if leftErr != nil || rightErr != nil {
			return false, ErrApprovedBuild
		}
	}
}
