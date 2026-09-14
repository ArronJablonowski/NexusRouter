package githubpublish

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestCredentialedPublishReleaseStopsOnConflictingRemoteState(t *testing.T) {
	for _, scenario := range []struct {
		name          string
		tagStatus     int
		releaseStatus int
		wantPaths     []string
	}{
		{
			name: "existing_annotated_tag_wrong_commit_no_release", tagStatus: http.StatusOK,
			releaseStatus: http.StatusNotFound,
			wantPaths:     []string{"/repos/ArronJablonowski/DarwinRouter/immutable-releases", "/repos/ArronJablonowski/DarwinRouter/git/ref/tags/v1.0.0"},
		},
		{
			name: "existing_release", tagStatus: http.StatusNotFound, releaseStatus: http.StatusOK,
			wantPaths: []string{"/repos/ArronJablonowski/DarwinRouter/immutable-releases", "/repos/ArronJablonowski/DarwinRouter/git/ref/tags/v1.0.0", "/repos/ArronJablonowski/DarwinRouter/releases/tags/v1.0.0"},
		},
		{
			name: "ambiguous_tag_lookup", tagStatus: http.StatusInternalServerError,
			releaseStatus: http.StatusNotFound,
			wantPaths:     []string{"/repos/ArronJablonowski/DarwinRouter/immutable-releases", "/repos/ArronJablonowski/DarwinRouter/git/ref/tags/v1.0.0"},
		},
		{
			name: "ambiguous_release_lookup", tagStatus: http.StatusNotFound,
			releaseStatus: http.StatusInternalServerError,
			wantPaths:     []string{"/repos/ArronJablonowski/DarwinRouter/immutable-releases", "/repos/ArronJablonowski/DarwinRouter/git/ref/tags/v1.0.0", "/repos/ArronJablonowski/DarwinRouter/releases/tags/v1.0.0"},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			plan := publicationPlan(t)
			plan.Owner, plan.Repository = "ArronJablonowski", "DarwinRouter"
			plan.Tag, plan.Commit = "v1.0.0", "fb8abf91d4e0c3a112f76ebe452acd9a1ac193a2"
			plan.Name, plan.TagMessage, plan.Prerelease = "DarwinRouter v1.0.0", "DarwinRouter release v1.0.0", false
			bodySecret := "release-asset-body-must-not-leak"
			plan.Assets[0].Body = []byte(bodySecret)
			plan.Assets[0].SHA256 = digest(plan.Assets[0].Body)
			oldTagObject := "d3dbb322c2372ed4b0b3bd9de3d7a236be006574"
			oldCommit := "ca07106cae194a5f02226f1e40fef0348d70f59d"
			// This models DAR-97's exact remote graph. Seeing the ref must stop
			// before the absent-release or peeled-tag endpoints are consulted.
			if oldCommit == plan.Commit {
				t.Fatal("conflict fixture must peel to a different commit")
			}

			token := []byte("github_pat_dar105_secret")
			secret := &secretFixture{credential: Credential{Token: token, ContentsWrite: true, AdministrationRead: true}}
			var paths []string
			mutations, leakedBodies, badCredentials := 0, 0, 0
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				paths = append(paths, request.URL.Path)
				if request.Method != http.MethodGet {
					mutations++
				}
				if request.Header.Get("Authorization") != "Bearer github_pat_dar105_secret" {
					badCredentials++
				}
				if request.Body != nil {
					body, err := io.ReadAll(request.Body)
					if err != nil || len(body) != 0 || strings.Contains(string(body), bodySecret) {
						leakedBodies++
					}
				}

				status, responseBody := http.StatusMethodNotAllowed, `{"message":"unexpected"}`
				switch request.URL.Path {
				case "/repos/ArronJablonowski/DarwinRouter/immutable-releases":
					status, responseBody = http.StatusOK, `{"enabled":true}`
				case "/repos/ArronJablonowski/DarwinRouter/git/ref/tags/" + plan.Tag:
					status = scenario.tagStatus
					responseBody = `{"ref":"refs/tags/` + plan.Tag + `","object":{"sha":"` + oldTagObject + `","type":"tag"}}`
				case "/repos/ArronJablonowski/DarwinRouter/releases/tags/" + plan.Tag:
					status = scenario.releaseStatus
					responseBody = `{"id":41,"tag_name":"` + plan.Tag + `"}`
				case "/repos/ArronJablonowski/DarwinRouter/git/tags/" + oldTagObject:
					status = http.StatusOK
					responseBody = `{"sha":"` + oldTagObject + `","object":{"sha":"` + oldCommit + `","type":"commit"}}`
				}
				return &http.Response{
					StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}},
					Body: io.NopCloser(strings.NewReader(responseBody)), Request: request,
				}, nil
			})

			evidence, err := PublishCredentialedRelease(t.Context(), CredentialPublicationConfig{
				APIBase: "https://api.github.com", UploadBase: "https://uploads.github.com",
				Transport: transport, Secrets: secret,
			}, plan)
			if !errors.Is(err, ErrPublish) || evidence.State != "not_started" || !evidence.RetryAllowed ||
				evidence.Published || evidence.TagObjectSHA != "" || evidence.ReleaseID != 0 {
				t.Fatal("conflicting remote state did not stop before publication", evidence, err)
			}
			if strings.Join(paths, "\n") != strings.Join(scenario.wantPaths, "\n") || mutations != 0 {
				t.Fatal("remote conflict reached an unexpected or mutating request", paths, mutations)
			}
			if leakedBodies != 0 || badCredentials != 0 || secret.calls != 1 {
				t.Fatal("preflight request leaked a body or escaped its credential lease", leakedBodies, badCredentials, secret.calls)
			}
			if _, statErr := os.Lstat(plan.JournalPath); !os.IsNotExist(statErr) {
				t.Fatal("conflict created a publication journal", statErr)
			}
			encoded, marshalErr := json.Marshal(evidence)
			if marshalErr != nil || strings.Contains(string(encoded), "github_pat") || strings.Contains(string(encoded), bodySecret) ||
				strings.Contains(err.Error(), "github_pat") || strings.Contains(err.Error(), bodySecret) {
				t.Fatal("failure evidence exposed credential or release body", string(encoded), err, marshalErr)
			}
			for _, value := range token {
				if value != 0 {
					t.Fatal("credential bytes survived the bounded operation")
				}
			}
		})
	}
}
