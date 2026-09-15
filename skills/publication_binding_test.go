package skills

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPublicationBindingExactRetryRestartAndReadOnly(t *testing.T) {
	ctx := context.Background()
	path := testPath(t)
	store := openTest(t, path)
	attempt := generationAttemptFixture("drafted")
	beforeAttempt, err := json.Marshal(attempt)
	if err != nil {
		t.Fatal(err)
	}
	version, err := store.PublishGeneration(ctx, attempt, false)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := GenerationAttemptDigest(attempt)
	if err != nil {
		t.Fatal(err)
	}
	want := PublicationBinding{Version: 1, Key: attempt.Key, SkillVersion: version.ID, AttemptID: attempt.ID, AttemptDigest: wantDigest}
	first, err := store.PublicationBinding(ctx, attempt.Key, version.ID)
	if err != nil || !reflect.DeepEqual(first, want) || first.Validate() != nil {
		t.Fatal("unexpected binding", first, err)
	}
	republished, err := store.PublishGeneration(ctx, attempt, false)
	if err != nil || !reflect.DeepEqual(republished, version) {
		t.Fatal("exact publication retry changed version", republished, err)
	}
	retry, err := store.PublicationBinding(ctx, attempt.Key, version.ID)
	if err != nil || !reflect.DeepEqual(retry, first) {
		t.Fatal("lookup retry changed binding", retry, err)
	}
	afterAttempt, _ := json.Marshal(attempt)
	if string(beforeAttempt) != string(afterAttempt) {
		t.Fatal("digest or lookup mutated generation attempt")
	}

	files := activationFiles(t, path)
	readOnly, err := OpenReadOnly(path, []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	restarted, err := readOnly.PublicationBinding(ctx, attempt.Key, version.ID)
	if err != nil || !reflect.DeepEqual(restarted, want) {
		t.Fatal("read-only restart changed binding", restarted, err)
	}
	if !reflect.DeepEqual(files, activationFiles(t, path)) {
		t.Fatal("publication lookup mutated store")
	}
}

func TestPublicationBindingRejectsMissingHandwrittenMismatchAndCancellation(t *testing.T) {
	path := testPath(t)
	store := openTest(t, path)
	ctx := context.Background()
	handwritten, err := store.Draft(ctx, sample(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.PublicationBinding(ctx, handwritten.Draft.Key, handwritten.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("handwritten draft received publication provenance", err)
	}

	attempt := generationAttemptFixture("drafted")
	published, err := store.PublishGeneration(ctx, attempt, false)
	if err != nil {
		t.Fatal(err)
	}
	other := sample()
	other.Key.Name = "other"
	otherVersion, err := store.Draft(ctx, other, false)
	if err != nil {
		t.Fatal(err)
	}
	before := activationFiles(t, path)
	for name, request := range map[string]struct {
		key     Key
		version string
	}{
		"unpublished-version": {attempt.Key, handwritten.ID},
		"wrong-key":           {other.Key, published.ID},
		"other-version":       {attempt.Key, otherVersion.ID},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.PublicationBinding(ctx, request.key, request.version); !errors.Is(err, ErrNotFound) {
				t.Fatal("mismatched binding accepted", err)
			}
		})
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = store.PublicationBinding(canceled, attempt.Key, published.ID); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation not preserved", err)
	}
	if _, err = store.PublicationBinding(nil, attempt.Key, published.ID); !errors.Is(err, ErrInvalid) {
		t.Fatal("nil context accepted", err)
	}
	var nilStore *FileStore
	if _, err = nilStore.PublicationBinding(ctx, attempt.Key, published.ID); !errors.Is(err, ErrInvalid) {
		t.Fatal("nil store accepted", err)
	}
	if !reflect.DeepEqual(before, activationFiles(t, path)) {
		t.Fatal("rejected publication lookups mutated store")
	}
}

func TestPublicationBindingRejectsCorruptionWithoutMutation(t *testing.T) {
	for _, mode := range []string{"digest", "duplicate", "missing-body", "body-mismatch", "invalid-version", "metadata-mismatch", "receipt-key"} {
		t.Run(mode, func(t *testing.T) {
			path := testPath(t)
			store := openTest(t, path)
			attempt := generationAttemptFixture("drafted")
			version, err := store.PublishGeneration(context.Background(), attempt, false)
			if err != nil {
				t.Fatal(err)
			}
			var c catalog
			if err = store.read("catalog.json", &c); err != nil {
				t.Fatal(err)
			}
			receipt := c.Publications[attempt.ID]
			switch mode {
			case "digest":
				receipt.AttemptDigest = strings.Repeat("A", 64)
				c.Publications[attempt.ID] = receipt
			case "duplicate":
				c.Publications["other-attempt"] = receipt
			case "missing-body":
				if err = os.Remove(filepath.Join(path, "version-"+version.ID+".json")); err != nil {
					t.Fatal(err)
				}
			case "body-mismatch":
				version.Draft.Description = "forged body"
				writeJSONFile(t, filepath.Join(path, "version-"+version.ID+".json"), version)
			case "invalid-version":
				version.Parent = "not-a-version"
				entry := c.Skills[attempt.Key.index()]
				entry.Versions[0].Digest = digest(version)
				c.Skills[attempt.Key.index()] = entry
				writeJSONFile(t, filepath.Join(path, "version-"+version.ID+".json"), version)
			case "metadata-mismatch":
				entry := c.Skills[attempt.Key.index()]
				entry.Versions[0].Description = "forged metadata"
				c.Skills[attempt.Key.index()] = entry
			case "receipt-key":
				receipt.Key.Name = "other"
				c.Publications[attempt.ID] = receipt
			}
			if mode != "missing-body" && mode != "body-mismatch" {
				writeJSONFile(t, filepath.Join(path, "catalog.json"), c)
			}
			before := activationFiles(t, path)
			if _, err = store.PublicationBinding(context.Background(), attempt.Key, version.ID); err == nil {
				t.Fatal("corrupt publication binding accepted")
			}
			if !reflect.DeepEqual(before, activationFiles(t, path)) {
				t.Fatal("corrupt publication lookup changed files")
			}
		})
	}
}

func TestGenerationAttemptDigestCanonicalAndValidated(t *testing.T) {
	attempt := generationAttemptFixture("drafted")
	want, err := GenerationAttemptDigest(attempt)
	if err != nil || !validHexDigest(want) {
		t.Fatal(want, err)
	}
	equivalent := attempt
	equivalent.StartedAt = equivalent.StartedAt.In(time.FixedZone("zero", 0))
	equivalent.FinishedAt = equivalent.FinishedAt.In(time.FixedZone("zero", 0))
	got, err := GenerationAttemptDigest(equivalent)
	if err != nil || got != want {
		t.Fatal("equivalent timestamps changed digest", got, want, err)
	}
	invalid := attempt
	invalid.ID = "../invalid"
	if got, err = GenerationAttemptDigest(invalid); !errors.Is(err, ErrInvalid) || got != "" {
		t.Fatal("invalid attempt received digest", got, err)
	}
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
}
