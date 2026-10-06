package githubverify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/internal/processaudit"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	MinimumReleaseAttestationVerifierVersion = "2.93.0"
	ReleaseAttestationPredicateType          = "https://in-toto.io/attestation/release/v0.1"
	ReleaseAttestationSigner                 = "https://dotcom.releases.github.com"
	ReleaseAttestationIssuer                 = "https://token.actions.githubusercontent.com"
	releaseVerificationResultMediaType       = "application/vnd.dev.sigstore.verificationresult+json;version=0.1"
	releaseBundleMediaType                   = "application/vnd.dev.sigstore.bundle.v0.3+json"
	maxVerifierBinary                        = 256 << 20
	maxVerifierOutput                        = 1 << 20
	maxVerifierError                         = 64 << 10
)

var ghVersionRE = regexp.MustCompile(`^gh version ([0-9]+)\.([0-9]+)\.([0-9]+)(?: \([^\r\n]*\))?\r?$`)

// ReleaseAttestationVerifier cryptographically verifies GitHub's release
// attestation before any remote asset is written locally.
type ReleaseAttestationVerifier interface {
	Verify(context.Context, ReleaseAttestationPlan) (ReleaseAttestationEvidence, error)
}

type ReleaseAttestationPlan struct {
	Repository   string
	Tag          string
	ReleaseID    int64
	TagObjectSHA string
	Assets       []ExpectedAsset
}

// ReleaseAttestationEvidence contains only normalized public verification
// identities. It excludes executable paths, authentication and raw gh output.
type ReleaseAttestationEvidence struct {
	VerifierVersion      string
	VerifierBinarySHA256 string
	VerifiedResultSHA256 string
	BundleSHA256         string
	Signer               string
	Issuer               string
	PredicateType        string
	TimestampCount       int
	TagSubjectDigest     string
}

type attestationCommand func(context.Context, string, ...string) ([]byte, error)

type commandReleaseAttestationVerifier struct {
	binary   string
	digest   string
	identity os.FileInfo
	run      attestationCommand
}

// NewCommandReleaseAttestationVerifier constructs a verifier around one exact,
// explicitly resolved gh binary. The binary must remain the same regular file
// with the same independently supplied digest throughout verification.
func NewCommandReleaseAttestationVerifier(binary, expectedSHA256 string) (ReleaseAttestationVerifier, error) {
	return newCommandReleaseAttestationVerifier(binary, expectedSHA256, runAttestationCommand)
}

func newCommandReleaseAttestationVerifier(binary, expectedSHA256 string, run attestationCommand) (ReleaseAttestationVerifier, error) {
	if !filepath.IsAbs(binary) || filepath.Clean(binary) != binary || !digestRE.MatchString(expectedSHA256) || run == nil {
		return nil, ErrVerify
	}
	resolved, err := filepath.EvalSymlinks(binary)
	if err != nil || resolved != binary {
		return nil, ErrVerify
	}
	identity, digest, err := commandBinaryIdentity(binary)
	if err != nil || digest != expectedSHA256 {
		return nil, ErrVerify
	}
	return &commandReleaseAttestationVerifier{binary: binary, digest: expectedSHA256, identity: identity, run: run}, nil
}

func (v *commandReleaseAttestationVerifier) Verify(ctx context.Context, plan ReleaseAttestationPlan) (ReleaseAttestationEvidence, error) {
	var empty ReleaseAttestationEvidence
	if ctx == nil || validateAttestationPlan(plan) != nil || v.checkBinary() != nil {
		return empty, ErrVerify
	}
	commandContext, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	versionOutput, versionErr := v.run(commandContext, v.binary, "--version")
	if versionErr != nil || v.checkBinary() != nil || len(versionOutput) > 4096 {
		return empty, ErrVerify
	}
	version, err := supportedGHVersion(versionOutput)
	if err != nil {
		return empty, ErrVerify
	}
	output, commandErr := v.run(commandContext, v.binary, "release", "verify", plan.Tag, "-R", "github.com/"+plan.Repository, "--format", "json")
	identityErr := v.checkBinary()
	if commandErr != nil || identityErr != nil || commandContext.Err() != nil {
		return empty, ErrVerify
	}
	evidence, err := parseReleaseAttestation(output, plan)
	if err != nil {
		return empty, ErrVerify
	}
	evidence.VerifierVersion = version
	evidence.VerifierBinarySHA256 = v.digest
	if !validReleaseAttestationEvidence(evidence, plan.TagObjectSHA) {
		return empty, ErrVerify
	}
	return evidence, nil
}

func validateAttestationPlan(plan ReleaseAttestationPlan) error {
	owner, repository, ok := strings.Cut(plan.Repository, "/")
	if !ok || !nameRE.MatchString(owner) || !nameRE.MatchString(repository) || !nameRE.MatchString(plan.Tag) ||
		plan.ReleaseID < 1 || !commitRE.MatchString(plan.TagObjectSHA) || len(plan.Assets) != 7 {
		return ErrVerify
	}
	previous := ""
	for _, asset := range plan.Assets {
		if !nameRE.MatchString(asset.Name) || asset.Name <= previous || !digestRE.MatchString(asset.SHA256) {
			return ErrVerify
		}
		previous = asset.Name
	}
	return nil
}

func (v *commandReleaseAttestationVerifier) checkBinary() error {
	identity, digest, err := commandBinaryIdentity(v.binary)
	if err != nil || !os.SameFile(v.identity, identity) || digest != v.digest {
		return ErrVerify
	}
	return nil
}

func commandBinaryIdentity(path string) (os.FileInfo, string, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0111 == 0 || before.Size() < 1 || before.Size() > maxVerifierBinary {
		return nil, "", ErrVerify
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", ErrVerify
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || after.Size() != before.Size() {
		return nil, "", ErrVerify
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, maxVerifierBinary+1))
	if err != nil || n != before.Size() {
		return nil, "", ErrVerify
	}
	return before, "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if len(p) > remaining {
		b.overflow = true
	}
	if remaining > len(p) {
		remaining = len(p)
	}
	if remaining > 0 {
		_, _ = b.buffer.Write(p[:remaining])
	}
	return len(p), nil
}

func runAttestationCommand(ctx context.Context, binary string, args ...string) ([]byte, error) {
	var stdout, stderr boundedBuffer
	stdout.limit, stderr.limit = maxVerifierOutput, maxVerifierError
	command := exec.CommandContext(ctx, binary, args...)
	command.Stdout, command.Stderr = &stdout, &stderr
	err := processaudit.Run(command)
	if err != nil || stdout.overflow || stderr.overflow || ctx.Err() != nil {
		return nil, ErrVerify
	}
	return bytes.Clone(stdout.buffer.Bytes()), nil
}

func supportedGHVersion(output []byte) (string, error) {
	lines := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	if len(lines) == 0 {
		return "", ErrVerify
	}
	match := ghVersionRE.FindStringSubmatch(lines[0])
	if match == nil {
		return "", ErrVerify
	}
	version := match[1] + "." + match[2] + "." + match[3]
	if compareVersion(match[1:4], strings.Split(MinimumReleaseAttestationVerifierVersion, ".")) < 0 {
		return "", ErrVerify
	}
	return version, nil
}

func compareVersion(left, right []string) int {
	for i := 0; i < 3; i++ {
		l, leftErr := strconv.ParseUint(left[i], 10, 32)
		r, rightErr := strconv.ParseUint(right[i], 10, 32)
		if leftErr != nil || rightErr != nil {
			return -1
		}
		if l < r {
			return -1
		}
		if l > r {
			return 1
		}
	}
	return 0
}

type ghAttestationOutput struct {
	Attestation struct {
		Bundle    json.RawMessage `json:"bundle"`
		BundleURL string          `json:"bundle_url"`
		Initiator string          `json:"initiator"`
	} `json:"attestation"`
	VerificationResult json.RawMessage `json:"verificationResult"`
}

type verificationResult struct {
	MediaType          string            `json:"mediaType"`
	Statement          json.RawMessage   `json:"statement"`
	Signature          signatureResult   `json:"signature"`
	VerifiedTimestamps []json.RawMessage `json:"verifiedTimestamps"`
	VerifiedIdentity   json.RawMessage   `json:"verifiedIdentity"`
}

type signatureResult struct {
	PublicKeyID json.RawMessage `json:"publicKeyId"`
	Certificate struct {
		CertificateIssuer      string `json:"certificateIssuer"`
		SubjectAlternativeName string `json:"subjectAlternativeName"`
		Issuer                 string `json:"issuer"`
	} `json:"certificate"`
}

type releaseStatement struct {
	Type    string `json:"_type"`
	Subject []struct {
		Name   string            `json:"name"`
		URI    string            `json:"uri"`
		Digest map[string]string `json:"digest"`
	} `json:"subject"`
	PredicateType string `json:"predicateType"`
	Predicate     struct {
		OwnerID      string `json:"ownerId"`
		PURL         string `json:"purl"`
		ReleaseID    string `json:"releaseId"`
		Repository   string `json:"repository"`
		RepositoryID string `json:"repositoryId"`
		Tag          string `json:"tag"`
	} `json:"predicate"`
}

func parseReleaseAttestation(body []byte, plan ReleaseAttestationPlan) (ReleaseAttestationEvidence, error) {
	var empty ReleaseAttestationEvidence
	if len(body) == 0 || len(body) > maxVerifierOutput {
		return empty, ErrVerify
	}
	var output ghAttestationOutput
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&output) != nil {
		return empty, ErrVerify
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || output.Attestation.Initiator != "github" || len(output.Attestation.Bundle) == 0 || len(output.VerificationResult) == 0 {
		return empty, ErrVerify
	}
	var verified verificationResult
	if json.Unmarshal(output.VerificationResult, &verified) != nil || verified.MediaType != releaseVerificationResultMediaType ||
		!validVerifiedTimestamps(verified.VerifiedTimestamps) ||
		verified.Signature.Certificate.SubjectAlternativeName != ReleaseAttestationSigner ||
		!validEvidenceText(verified.Signature.Certificate.CertificateIssuer, 512) ||
		verified.Signature.Certificate.Issuer != ReleaseAttestationIssuer {
		return empty, ErrVerify
	}
	var bundle struct {
		MediaType    string `json:"mediaType"`
		DSSEEnvelope struct {
			Payload     string `json:"payload"`
			PayloadType string `json:"payloadType"`
		} `json:"dsseEnvelope"`
	}
	if json.Unmarshal(output.Attestation.Bundle, &bundle) != nil || bundle.MediaType != releaseBundleMediaType ||
		bundle.DSSEEnvelope.PayloadType != "application/vnd.in-toto+json" {
		return empty, ErrVerify
	}
	payload, err := base64.StdEncoding.DecodeString(bundle.DSSEEnvelope.Payload)
	if err != nil || !equalJSON(payload, verified.Statement) {
		return empty, ErrVerify
	}
	var statement releaseStatement
	if json.Unmarshal(verified.Statement, &statement) != nil || validateReleaseStatement(statement, plan) != nil {
		return empty, ErrVerify
	}
	resultDigest, err := compactJSONDigest(output.VerificationResult)
	if err != nil {
		return empty, ErrVerify
	}
	bundleDigest, err := compactJSONDigest(output.Attestation.Bundle)
	if err != nil {
		return empty, ErrVerify
	}
	return ReleaseAttestationEvidence{
		VerifiedResultSHA256: resultDigest, BundleSHA256: bundleDigest,
		Signer: verified.Signature.Certificate.SubjectAlternativeName,
		Issuer: verified.Signature.Certificate.Issuer, PredicateType: statement.PredicateType,
		TimestampCount: len(verified.VerifiedTimestamps), TagSubjectDigest: "sha1:" + plan.TagObjectSHA,
	}, nil
}

func validVerifiedTimestamps(timestamps []json.RawMessage) bool {
	if len(timestamps) < 1 || len(timestamps) > 1_000_000 {
		return false
	}
	for _, raw := range timestamps {
		var timestamp struct {
			Type      string `json:"type"`
			URI       string `json:"uri"`
			Timestamp string `json:"timestamp"`
		}
		if json.Unmarshal(raw, &timestamp) != nil || !validEvidenceText(timestamp.Type, 64) ||
			!validEvidenceText(timestamp.URI, 2048) || timeValue(timestamp.Timestamp) == nil {
			return false
		}
	}
	return true
}

func timeValue(value string) *time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	return &parsed
}

func validateReleaseStatement(statement releaseStatement, plan ReleaseAttestationPlan) error {
	if statement.Type != "https://in-toto.io/Statement/v1" || statement.PredicateType != ReleaseAttestationPredicateType ||
		statement.Predicate.Repository != plan.Repository || statement.Predicate.Tag != plan.Tag ||
		statement.Predicate.PURL != "pkg:github/"+plan.Repository+"@"+plan.Tag ||
		!positiveDecimal(statement.Predicate.OwnerID) || !positiveDecimal(statement.Predicate.RepositoryID) ||
		statement.Predicate.ReleaseID != strconv.FormatInt(plan.ReleaseID, 10) || len(statement.Subject) != len(plan.Assets)+1 {
		return ErrVerify
	}
	tagURI := "pkg:github/" + plan.Repository + "@" + plan.Tag
	assets := make(map[string]string, len(plan.Assets))
	for _, asset := range plan.Assets {
		assets[asset.Name] = strings.TrimPrefix(asset.SHA256, "sha256:")
	}
	tagFound := false
	for _, subject := range statement.Subject {
		if subject.URI != "" {
			if tagFound || subject.Name != "" || subject.URI != tagURI || len(subject.Digest) != 1 || subject.Digest["sha1"] != plan.TagObjectSHA {
				return ErrVerify
			}
			tagFound = true
			continue
		}
		want, ok := assets[subject.Name]
		if !ok || len(subject.Digest) != 1 || subject.Digest["sha256"] != want {
			return ErrVerify
		}
		delete(assets, subject.Name)
	}
	if !tagFound || len(assets) != 0 {
		return ErrVerify
	}
	return nil
}

func positiveDecimal(value string) bool {
	parsed, err := strconv.ParseUint(value, 10, 64)
	return err == nil && parsed > 0 && strconv.FormatUint(parsed, 10) == value
}

func equalJSON(left, right []byte) bool {
	var l, r any
	if json.Unmarshal(left, &l) != nil || json.Unmarshal(right, &r) != nil {
		return false
	}
	lb, leftErr := json.Marshal(l)
	rb, rightErr := json.Marshal(r)
	return leftErr == nil && rightErr == nil && bytes.Equal(lb, rb)
}

func compactJSONDigest(body []byte) (string, error) {
	var compact bytes.Buffer
	if json.Compact(&compact, body) != nil {
		return "", ErrVerify
	}
	digest := sha256.Sum256(compact.Bytes())
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func validReleaseAttestationEvidence(evidence ReleaseAttestationEvidence, tagObjectSHA string) bool {
	parts := strings.Split(evidence.VerifierVersion, ".")
	return len(parts) == 3 && compareVersion(parts, strings.Split(MinimumReleaseAttestationVerifierVersion, ".")) >= 0 &&
		digestRE.MatchString(evidence.VerifierBinarySHA256) && digestRE.MatchString(evidence.VerifiedResultSHA256) &&
		digestRE.MatchString(evidence.BundleSHA256) && evidence.Signer == ReleaseAttestationSigner &&
		evidence.Issuer == ReleaseAttestationIssuer && evidence.PredicateType == ReleaseAttestationPredicateType &&
		evidence.TimestampCount > 0 && evidence.TimestampCount <= 1_000_000 &&
		evidence.TagSubjectDigest == "sha1:"+tagObjectSHA
}

func validEvidenceText(value string, max int) bool {
	return len(value) > 0 && len(value) <= max && !strings.ContainsAny(value, "\x00\r\n")
}
