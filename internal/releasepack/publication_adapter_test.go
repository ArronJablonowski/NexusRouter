package releasepack

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/ArronJablonowski/DarwinRouter/internal/githubpublish"
)

type adapterSecretSource struct{ calls int }

func (s *adapterSecretSource) GitHubCredential(context.Context) (githubpublish.Credential, error) {
	s.calls++
	return githubpublish.Credential{Token: []byte("must-not-be-read"), ContentsWrite: true, AdministrationRead: true}, nil
}

type rejectingPublicationTransport struct{}

func (rejectingPublicationTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network must not be reached")
}

type authorizedPublisherFixture struct {
	calls int
	plan  githubpublish.PublicationPlan
}

func (p *authorizedPublisherFixture) PublishRelease(_ context.Context, plan githubpublish.PublicationPlan) (githubpublish.PublicationEvidence, error) {
	p.calls++
	p.plan = plan
	return githubpublish.PublicationEvidence{SchemaVersion: 1, State: "fixture"}, nil
}

func TestPublicationAdapterDerivesExactFullPlanAfterClosingChecks(t *testing.T) {
	preflight, _ := publishedFixture(t)
	want, err := VerifyPublicationPreflight(t.Context(), preflight)
	if err != nil {
		t.Fatal(err)
	}
	publisher := &authorizedPublisherFixture{}
	journal := filepath.Join(t.TempDir(), "publication.jsonl")
	if _, err = PublishAuthorizedRelease(t.Context(), publisher, AuthorizedPublicationOptions{Preflight: preflight, JournalPath: journal}); err != nil {
		t.Fatal(err)
	}
	plan := publisher.plan
	if publisher.calls != 1 || plan.AuthorizationSHA256 != want.PublicationAuthorizationSHA256 ||
		plan.Owner+"/"+plan.Repository != want.Repository || plan.Tag != want.Tag || plan.Commit != want.SourceCommit ||
		plan.Name != want.ReleaseTitle || plan.TagMessage != want.TagMessage || plan.MakeLatest != want.MakeLatest ||
		plan.Tagger != (githubpublish.Tagger{Name: want.Tagger.Name, Email: want.Tagger.Email, Date: want.Tagger.Date}) ||
		plan.JournalPath != journal || len(plan.Assets) != 7 {
		t.Fatal("adapter did not preserve the exact authorized plan")
	}
	for i, asset := range plan.Assets {
		if asset.Name != want.Assets[i].Name || int64(len(asset.Body)) != want.Assets[i].Size || asset.SHA256 != want.Assets[i].SHA256 || publicationDigest(asset.Body) != asset.SHA256 || asset.ContentType != publicationContentType(asset.Name) {
			t.Fatal("adapter asset drift", asset.Name)
		}
	}
}

func TestPublicationAdapterMakesZeroPublisherCallsOnBoundaryMutation(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, PublicationPreflightOptions, string){
		"release_asset_mutated": func(t *testing.T, options PublicationPreflightOptions, _ string) {
			entries, err := os.ReadDir(options.Verification.Dir)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(options.Verification.Dir, entries[0].Name()), []byte("mutated\n"), 0600); err != nil {
				t.Fatal(err)
			}
		},
		"authorization_replaced": func(t *testing.T, options PublicationPreflightOptions, _ string) {
			replaceSameBytes(t, options.PublicationAuthorizationFile)
		},
		"release_root_replaced": func(t *testing.T, options PublicationPreflightOptions, _ string) {
			old := options.Verification.Dir + ".old"
			if err := os.Rename(options.Verification.Dir, old); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(options.Verification.Dir, 0700); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(old)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				body, readErr := os.ReadFile(filepath.Join(old, entry.Name()))
				if readErr != nil || os.WriteFile(filepath.Join(options.Verification.Dir, entry.Name()), body, 0600) != nil {
					t.Fatal("failed to replace release root")
				}
			}
		},
		"journal_claimed": func(t *testing.T, _ PublicationPreflightOptions, journal string) {
			if err := os.WriteFile(journal, []byte("claimed\n"), 0600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			preflight, _ := publishedFixture(t)
			journal := filepath.Join(t.TempDir(), "publication.jsonl")
			publisher := &authorizedPublisherFixture{}
			calls := 0
			verifier := func(ctx context.Context, options PublicationPreflightOptions) (PublicationPreflightResult, error) {
				calls++
				result, err := VerifyPublicationPreflight(ctx, options)
				if calls == 2 && err == nil {
					mutate(t, options, journal)
				}
				return result, err
			}
			_, err := publishAuthorizedReleaseWithVerifier(t.Context(), publisher, AuthorizedPublicationOptions{Preflight: preflight, JournalPath: journal}, verifier)
			if err == nil || publisher.calls != 0 {
				t.Fatal("boundary mutation reached publisher", err, publisher.calls)
			}
		})
	}
}

func TestPublicationAdapterRejectsSymlinkBeforePreflight(t *testing.T) {
	preflight, _ := publishedFixture(t)
	link := filepath.Join(t.TempDir(), "authorization.json")
	if err := os.Symlink(preflight.PublicationAuthorizationFile, link); err != nil {
		t.Fatal(err)
	}
	preflight.PublicationAuthorizationFile = link
	publisher := &authorizedPublisherFixture{}
	called := false
	verifier := func(context.Context, PublicationPreflightOptions) (PublicationPreflightResult, error) {
		called = true
		return PublicationPreflightResult{}, nil
	}
	_, err := publishAuthorizedReleaseWithVerifier(t.Context(), publisher, AuthorizedPublicationOptions{Preflight: preflight, JournalPath: filepath.Join(t.TempDir(), "journal")}, verifier)
	if err == nil || called || publisher.calls != 0 {
		t.Fatal("symlink reached preflight or publisher")
	}
}

func TestPublicationAdapterRejectsJournalInsideVerifiedRoots(t *testing.T) {
	for _, root := range []string{"source", "release"} {
		t.Run(root, func(t *testing.T) {
			preflight, _ := publishedFixture(t)
			parent := preflight.Verification.Source
			if root == "release" {
				parent = preflight.Verification.Dir
			}
			publisher := &authorizedPublisherFixture{}
			called := false
			verifier := func(context.Context, PublicationPreflightOptions) (PublicationPreflightResult, error) {
				called = true
				return PublicationPreflightResult{}, nil
			}
			_, err := publishAuthorizedReleaseWithVerifier(t.Context(), publisher, AuthorizedPublicationOptions{
				Preflight: preflight, JournalPath: filepath.Join(parent, "publication-operation.jsonl"),
			}, verifier)
			if err == nil || called || publisher.calls != 0 {
				t.Fatal("journal inside verified root reached preflight or publisher")
			}
		})
	}
}

func TestCredentialAcquisitionFollowsClosingPreflight(t *testing.T) {
	preflight, _ := publishedFixture(t)
	secret := &adapterSecretSource{}
	config := githubpublish.CredentialPublicationConfig{
		APIBase: "https://api.github.com", UploadBase: "https://uploads.github.com",
		Transport: rejectingPublicationTransport{}, Secrets: secret,
	}
	failed := preflight
	failed.ExpectedRepository = "other/repository"
	if _, err := PublishAuthorizedReleaseWithCredential(t.Context(), config, AuthorizedPublicationOptions{
		Preflight: failed, JournalPath: filepath.Join(t.TempDir(), "failed.jsonl"),
	}); err == nil || secret.calls != 0 {
		t.Fatal("failed preflight acquired a credential", err, secret.calls)
	}
	entries, err := os.ReadDir(preflight.Verification.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(preflight.Verification.Dir, entries[0].Name()), []byte("tampered\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = PublishAuthorizedReleaseWithCredential(t.Context(), config, AuthorizedPublicationOptions{
		Preflight: preflight, JournalPath: filepath.Join(t.TempDir(), "tampered.jsonl"),
	}); err == nil || secret.calls != 0 {
		t.Fatal("tampered preflight acquired a credential", err, secret.calls)
	}
}

func replaceSameBytes(t *testing.T, path string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := path + ".replacement"
	if err = os.WriteFile(replacement, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
}
