package githubverify

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCommandReleaseAttestationVerifier(t *testing.T) {
	plan := releaseAttestationPlanFixture()
	body := releaseAttestationOutput(t, plan, nil)
	verifier, binary := commandAttestorFixture(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if reflect.DeepEqual(args, []string{"--version"}) {
			return []byte("gh version 2.98.0 (2026-08-20)\nhttps://github.com/cli/cli/releases/tag/v2.98.0\n"), nil
		}
		want := []string{"release", "verify", plan.Tag, "-R", "github.com/" + plan.Repository, "--format", "json"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("unexpected gh arguments: %#v", args)
		}
		return body, nil
	})
	evidence, err := verifier.Verify(context.Background(), plan)
	if err != nil || evidence.VerifierVersion != "2.98.0" || evidence.Signer != ReleaseAttestationSigner ||
		evidence.PredicateType != ReleaseAttestationPredicateType || evidence.TimestampCount != 1 ||
		evidence.TagSubjectDigest != "sha1:"+plan.TagObjectSHA || !digestRE.MatchString(evidence.VerifierBinarySHA256) ||
		!digestRE.MatchString(evidence.VerifiedResultSHA256) || !digestRE.MatchString(evidence.BundleSHA256) {
		t.Fatal("valid command attestation rejected", binary, evidence, err)
	}
}

func TestCommandReleaseAttestationRejectsInvalidResults(t *testing.T) {
	plan := releaseAttestationPlanFixture()
	mutations := map[string]func(map[string]any){
		"absent":             func(output map[string]any) { clear(output) },
		"bad_result":         func(output map[string]any) { output["verificationResult"] = "not-an-object" },
		"result_media_type":  func(output map[string]any) { resultMap(output)["mediaType"] = "application/json" },
		"bundle_media_type":  func(output map[string]any) { bundleMap(output)["mediaType"] = "application/json" },
		"initiator":          func(output map[string]any) { attestationMap(output)["initiator"] = "user" },
		"signer":             func(output map[string]any) { certificateMap(output)["subjectAlternativeName"] = "https://evil.example" },
		"certificate_issuer": func(output map[string]any) { certificateMap(output)["certificateIssuer"] = "" },
		"oidc_issuer": func(output map[string]any) {
			certificateMap(output)["issuer"] = "https://evil.example"
		},
		"timestamps": func(output map[string]any) { resultMap(output)["verifiedTimestamps"] = []any{} },
		"null_timestamp": func(output map[string]any) {
			resultMap(output)["verifiedTimestamps"] = []any{nil}
		},
		"bad_timestamp": func(output map[string]any) {
			resultMap(output)["verifiedTimestamps"] = []any{map[string]any{"type": "Tlog", "uri": "https://rekor.example/1", "timestamp": "not-a-time"}}
		},
		"predicate": func(output map[string]any) {
			statementMap(output)["predicateType"] = "https://example.invalid/predicate"
		},
		"repository": func(output map[string]any) { predicateMap(output)["repository"] = "other/repository" },
		"tag":        func(output map[string]any) { predicateMap(output)["tag"] = "v9.9.9" },
		"release":    func(output map[string]any) { predicateMap(output)["releaseId"] = "999" },
		"owner_id":   func(output map[string]any) { predicateMap(output)["ownerId"] = "0" },
		"repository_id": func(output map[string]any) {
			predicateMap(output)["repositoryId"] = "not-a-positive-decimal"
		},
		"tag_digest": func(output map[string]any) {
			subjectMaps(output)[0]["digest"] = map[string]any{"sha1": strings.Repeat("f", 40)}
		},
		"missing_asset": func(output map[string]any) {
			subjects := resultStatementSubjects(output)
			statementMap(output)["subject"] = subjects[:len(subjects)-1]
		},
		"extra_asset": func(output map[string]any) {
			subjects := resultStatementSubjects(output)
			statementMap(output)["subject"] = append(subjects, map[string]any{"name": "extra", "digest": map[string]any{"sha256": strings.Repeat("a", 64)}})
		},
		"duplicate_asset": func(output map[string]any) {
			subjects := resultStatementSubjects(output)
			statementMap(output)["subject"] = append(subjects, subjects[1])
		},
		"swapped_assets": func(output map[string]any) {
			subjects := subjectMaps(output)
			subjects[1]["digest"], subjects[2]["digest"] = subjects[2]["digest"], subjects[1]["digest"]
		},
		"dsse_mismatch": func(output map[string]any) {
			bundleMap(output)["dsseEnvelope"].(map[string]any)["payload"] = base64.StdEncoding.EncodeToString([]byte(`{"different":true}`))
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			body := releaseAttestationOutput(t, plan, mutate)
			verifier, _ := commandAttestorFixture(t, fixtureAttestationRunner(body, nil))
			if evidence, err := verifier.Verify(context.Background(), plan); err != ErrVerify || evidence != (ReleaseAttestationEvidence{}) {
				t.Fatal("invalid attestation accepted", evidence, err)
			}
		})
	}
}

func TestParseReleaseAttestationRejectsUnknownTrailingAndBoundedOutput(t *testing.T) {
	plan := releaseAttestationPlanFixture()
	valid := releaseAttestationOutput(t, plan, nil)
	var output map[string]any
	if err := json.Unmarshal(valid, &output); err != nil {
		t.Fatal(err)
	}
	output["unexpected"] = true
	unknown, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"unknown":  unknown,
		"trailing": append(valid, []byte(`{"second":true}`)...),
		"oversize": bytes.Repeat([]byte("x"), maxVerifierOutput+1),
	} {
		t.Run(name, func(t *testing.T) {
			if evidence, err := parseReleaseAttestation(body, plan); err != ErrVerify || evidence != (ReleaseAttestationEvidence{}) {
				t.Fatal("invalid JSON output accepted", evidence, err)
			}
		})
	}
}

func TestRunAttestationCommandBoundsStdoutAndStderr(t *testing.T) {
	for name, script := range map[string]string{
		"stdout": `yes x | head -c 1048577`,
		"stderr": `yes x | head -c 65537 >&2`,
	} {
		t.Run(name, func(t *testing.T) {
			if output, err := runAttestationCommand(context.Background(), "/bin/sh", "-c", script); err != ErrVerify || output != nil {
				t.Fatal("unbounded command output accepted", len(output), err)
			}
		})
	}
}

func TestCommandReleaseAttestationRejectsCommandVersionCancellationAndReplacement(t *testing.T) {
	plan := releaseAttestationPlanFixture()
	body := releaseAttestationOutput(t, plan, nil)
	for name, runner := range map[string]attestationCommand{
		"command": fixtureAttestationRunner(nil, errors.New("private command failure")),
		"old_version": func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) == 1 {
				return []byte("gh version 2.92.9 (old)\n"), nil
			}
			return body, nil
		},
		"malformed_version": func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) == 1 {
				return []byte("gh version development\n"), nil
			}
			return body, nil
		},
		"oversize_version": func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) == 1 {
				return bytes.Repeat([]byte("x"), 4097), nil
			}
			return body, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			verifier, _ := commandAttestorFixture(t, runner)
			if _, err := verifier.Verify(context.Background(), plan); err != ErrVerify {
				t.Fatal("invalid command state accepted", err)
			}
		})
	}
	t.Run("canceled", func(t *testing.T) {
		verifier, _ := commandAttestorFixture(t, fixtureAttestationRunner(body, nil))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := verifier.Verify(ctx, plan); err != ErrVerify {
			t.Fatal("canceled verification accepted", err)
		}
	})
	t.Run("binary_replaced", func(t *testing.T) {
		var binary string
		verifier, path := commandAttestorFixture(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) == 1 {
				return []byte("gh version 2.98.0 (current)\n"), nil
			}
			if err := os.WriteFile(binary, []byte("changed executable\n"), 0700); err != nil {
				t.Fatal(err)
			}
			return body, nil
		})
		binary = path
		if _, err := verifier.Verify(context.Background(), plan); err != ErrVerify {
			t.Fatal("replaced verifier binary accepted", err)
		}
	})
}

func TestCommandReleaseAttestationConstructorRejectsUnsafeBinary(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(binary, []byte("binary\n"), 0700); err != nil {
		t.Fatal(err)
	}
	_, digest, err := commandBinaryIdentity(binary)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "gh")
	if err = os.Symlink(binary, link); err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string][2]string{
		"relative": {"gh", digest}, "symlink": {link, digest},
		"digest": {binary, "sha256:" + strings.Repeat("0", 64)},
	} {
		t.Run(name, func(t *testing.T) {
			path, expected := test[0], test[1]
			if verifier, err := NewCommandReleaseAttestationVerifier(path, expected); err != ErrVerify || verifier != nil {
				t.Fatal("unsafe verifier binary accepted", verifier, err)
			}
		})
	}
}

func commandAttestorFixture(t *testing.T, runner attestationCommand) (ReleaseAttestationVerifier, string) {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(binary, []byte("synthetic gh executable\n"), 0700); err != nil {
		t.Fatal(err)
	}
	binary, err := filepath.EvalSymlinks(binary)
	if err != nil {
		t.Fatal(err)
	}
	_, digest, err := commandBinaryIdentity(binary)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := newCommandReleaseAttestationVerifier(binary, digest, runner)
	if err != nil {
		t.Fatal(err)
	}
	return verifier, binary
}

func fixtureAttestationRunner(body []byte, failure error) attestationCommand {
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if failure != nil {
			return nil, failure
		}
		if len(args) == 1 && args[0] == "--version" {
			return []byte("gh version 2.98.0 (current)\n"), nil
		}
		return body, nil
	}
}

func releaseAttestationPlanFixture() ReleaseAttestationPlan {
	assets := make([]ExpectedAsset, 0, 7)
	for i, name := range []string{"NexusRouter_1.0.0_darwin_amd64.tar.gz", "NexusRouter_1.0.0_darwin_arm64.tar.gz", "NexusRouter_1.0.0_linux_amd64.tar.gz", "NexusRouter_1.0.0_linux_arm64.tar.gz", "SHA256SUMS", "SHA256SUMS.sig", "manifest.json"} {
		assets = append(assets, ExpectedAsset{Name: name, SHA256: "sha256:" + strings.Repeat(string(rune('1'+i)), 64)})
	}
	return ReleaseAttestationPlan{Repository: "acme/router", Tag: "v1.0.0", ReleaseID: 41, TagObjectSHA: strings.Repeat("a", 40), Assets: assets}
}

func releaseAttestationOutput(t *testing.T, plan ReleaseAttestationPlan, mutate func(map[string]any)) []byte {
	t.Helper()
	subjects := []any{map[string]any{"uri": "pkg:github/" + plan.Repository + "@" + plan.Tag, "digest": map[string]any{"sha1": plan.TagObjectSHA}}}
	for _, asset := range plan.Assets {
		subjects = append(subjects, map[string]any{"name": asset.Name, "digest": map[string]any{"sha256": strings.TrimPrefix(asset.SHA256, "sha256:")}})
	}
	statement := map[string]any{
		"_type": "https://in-toto.io/Statement/v1", "subject": subjects, "predicateType": ReleaseAttestationPredicateType,
		"predicate": map[string]any{
			"ownerId": "11", "purl": "pkg:github/" + plan.Repository + "@" + plan.Tag,
			"releaseId": "41", "repository": plan.Repository, "repositoryId": "22", "tag": plan.Tag,
		},
	}
	statementBody, err := json.Marshal(statement)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]any{
		"mediaType": "application/vnd.dev.sigstore.verificationresult+json;version=0.1", "statement": statement,
		"signature": map[string]any{"certificate": map[string]any{
			"certificateIssuer":      "CN=Fulcio Intermediate,O=GitHub Inc.",
			"subjectAlternativeName": ReleaseAttestationSigner,
			"issuer":                 ReleaseAttestationIssuer,
		}},
		"verifiedTimestamps": []any{map[string]any{"type": "Tlog", "uri": "https://rekor.example/1", "timestamp": "2026-09-07T01:00:00Z"}},
	}
	bundle := map[string]any{"mediaType": "application/vnd.dev.sigstore.bundle.v0.3+json", "dsseEnvelope": map[string]any{"payloadType": "application/vnd.in-toto+json", "payload": base64.StdEncoding.EncodeToString(statementBody)}}
	output := map[string]any{"attestation": map[string]any{"bundle": bundle, "bundle_url": "https://example.invalid/bundle", "initiator": "github"}, "verificationResult": result}
	if mutate != nil {
		mutate(output)
		// Keep policy-drift cases internally DSSE-consistent so each test reaches
		// the named semantic check. The explicit dsse_mismatch mutation replaces
		// the original payload and is intentionally left inconsistent.
		if result, ok := output["verificationResult"].(map[string]any); ok {
			if changedStatement, ok := result["statement"].(map[string]any); ok {
				envelope := bundleMap(output)["dsseEnvelope"].(map[string]any)
				if envelope["payload"] == base64.StdEncoding.EncodeToString(statementBody) {
					changedBody, marshalErr := json.Marshal(changedStatement)
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					envelope["payload"] = base64.StdEncoding.EncodeToString(changedBody)
				}
			}
		}
	}
	body, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	return append(body, '\n')
}

func attestationMap(output map[string]any) map[string]any {
	return output["attestation"].(map[string]any)
}
func bundleMap(output map[string]any) map[string]any {
	return attestationMap(output)["bundle"].(map[string]any)
}
func resultMap(output map[string]any) map[string]any {
	return output["verificationResult"].(map[string]any)
}
func statementMap(output map[string]any) map[string]any {
	return resultMap(output)["statement"].(map[string]any)
}
func predicateMap(output map[string]any) map[string]any {
	return statementMap(output)["predicate"].(map[string]any)
}
func certificateMap(output map[string]any) map[string]any {
	return resultMap(output)["signature"].(map[string]any)["certificate"].(map[string]any)
}
func resultStatementSubjects(output map[string]any) []any {
	return statementMap(output)["subject"].([]any)
}
func subjectMaps(output map[string]any) []map[string]any {
	values := resultStatementSubjects(output)
	result := make([]map[string]any, len(values))
	for i := range values {
		result[i] = values[i].(map[string]any)
	}
	return result
}
