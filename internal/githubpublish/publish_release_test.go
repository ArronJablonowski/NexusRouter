package githubpublish

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPublishReleaseAnnotatedImmutableStateMachine(t *testing.T) {
	plan := publicationPlan(t)
	tagSHA := strings.Repeat("c", 40)
	var mu sync.Mutex
	tagCreated, refCreated, draftCreated, published := false, false, false, false
	uploaded := map[string]assetResponse{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		base := "/repos/acme/darwin"
		switch {
		case r.Method == http.MethodGet && r.URL.Path == base+"/immutable-releases":
			writeJSON(w, http.StatusOK, immutableReleasePolicy{Enabled: true})
		case r.Method == http.MethodGet && r.URL.Path == base+"/git/ref/tags/"+plan.Tag && !refCreated:
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		case r.Method == http.MethodGet && r.URL.Path == base+"/releases/tags/"+plan.Tag:
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		case r.Method == http.MethodPost && r.URL.Path == base+"/git/tags":
			var request annotatedTagRequest
			decodeRequest(t, r, &request)
			want := annotatedTagRequest{Tag: plan.Tag, Message: plan.TagMessage, Object: plan.Commit, Type: "commit", Tagger: plan.Tagger}
			if request != want || tagCreated {
				t.Error("inexact or repeated annotated tag", request)
			}
			tagCreated = true
			writeJSON(w, http.StatusCreated, annotatedFixture(plan, tagSHA))
		case r.Method == http.MethodPost && r.URL.Path == base+"/git/refs":
			var request struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			}
			decodeRequest(t, r, &request)
			if !tagCreated || refCreated || request.Ref != "refs/tags/"+plan.Tag || request.SHA != tagSHA {
				t.Error("tag ref did not bind annotated object", request)
			}
			refCreated = true
			writeJSON(w, http.StatusCreated, annotatedRefFixture(plan, tagSHA))
		case r.Method == http.MethodPost && r.URL.Path == base+"/releases":
			var request createReleaseRequest
			decodeRequest(t, r, &request)
			if !refCreated || request.Draft != true || request.TagName != plan.Tag || request.TargetCommitish != plan.Commit {
				t.Error("inexact draft request", request)
			}
			draftCreated = true
			writeJSON(w, http.StatusCreated, publicationReleaseFixture(serverURL(r), plan, true, false))
		case r.Method == http.MethodPost && r.URL.Path == base+"/releases/41/assets":
			asset := findAsset(t, plan.DraftPlan, r.URL.Query().Get("name"))
			body, _ := io.ReadAll(r.Body)
			if !draftCreated || published || string(body) != string(asset.Body) {
				t.Error("invalid upload")
			}
			remote := assetFixture(asset, int64(len(uploaded)+1))
			uploaded[asset.Name] = remote
			writeJSON(w, http.StatusCreated, remote)
		case r.Method == http.MethodPatch && r.URL.Path == base+"/releases/41":
			var request updateReleaseRequest
			decodeRequest(t, r, &request)
			want := updateReleaseRequest{TagName: plan.Tag, TargetCommitish: plan.Commit, Name: plan.Name, Body: plan.Body, Prerelease: plan.Prerelease, MakeLatest: "false"}
			if published || request != want || len(uploaded) != len(plan.Assets) {
				t.Error("inexact or repeated publish transition", request)
			}
			published = true
			writeJSON(w, http.StatusOK, publicationReleaseFixture(serverURL(r), plan, false, true))
		case r.Method == http.MethodGet && r.URL.Path == base+"/releases/41":
			writeJSON(w, http.StatusOK, publicationReleaseFixture(serverURL(r), plan, !published, published))
		case r.Method == http.MethodGet && r.URL.Path == base+"/releases/41/assets":
			result := make([]assetResponse, 0, len(plan.Assets))
			for _, asset := range plan.Assets {
				result = append(result, uploaded[asset.Name])
			}
			writeJSON(w, http.StatusOK, result)
		case r.Method == http.MethodGet && r.URL.Path == base+"/git/ref/tags/"+plan.Tag:
			writeJSON(w, http.StatusOK, annotatedRefFixture(plan, tagSHA))
		case r.Method == http.MethodGet && r.URL.Path == base+"/git/tags/"+tagSHA:
			writeJSON(w, http.StatusOK, annotatedFixture(plan, tagSHA))
		case r.Method == http.MethodGet && r.URL.Path == base+"/releases/latest":
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "rejected"})
		}
	}))
	defer server.Close()
	publisher := newTestPublisher(t, server)
	evidence, err := publisher.PublishRelease(t.Context(), plan)
	if err != nil || evidence.State != "confirmed_published" || !evidence.Published || !evidence.Immutable ||
		evidence.TagObjectSHA != tagSHA || evidence.ReleaseID != 41 || evidence.Requests != 19 {
		t.Fatal("publication failed", evidence, err)
	}
	journal, err := os.ReadFile(plan.JournalPath)
	if err != nil || !strings.Contains(string(journal), `"phase":"create_tag_object","outcome":"confirmed","object_sha":"`+tagSHA+`"`) ||
		!strings.Contains(string(journal), `"phase":"publish_release"`) || !strings.Contains(string(journal), `"type":"confirmation"`) {
		t.Fatal("journal omitted mutation evidence", string(journal), err)
	}
}

func TestPublishReleaseRequiresImmutablePolicyBeforeMutation(t *testing.T) {
	plan := publicationPlan(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		writeJSON(w, http.StatusOK, immutableReleasePolicy{Enabled: false})
	}))
	defer server.Close()
	evidence, err := newTestPublisher(t, server).PublishRelease(t.Context(), plan)
	if err == nil || evidence.State != "not_started" || !evidence.RetryAllowed || evidence.Requests != 1 || requests != 1 {
		t.Fatal("disabled immutable policy did not stop", evidence, err)
	}
	if _, statErr := os.Stat(plan.JournalPath); !os.IsNotExist(statErr) {
		t.Fatal("journal/mutation started before policy gate", statErr)
	}
}

func TestPublishReleaseLostMutationResponsesAreUncertain(t *testing.T) {
	for _, failedPhase := range []string{"create_tag_object", "create_tag_ref", "create_draft", "upload_asset", "publish_release"} {
		t.Run(failedPhase, func(t *testing.T) {
			plan := publicationPlan(t)
			tagSHA := strings.Repeat("c", 40)
			uploaded := map[string]assetResponse{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				base := "/repos/acme/darwin"
				phase := ""
				switch {
				case r.Method == http.MethodPost && r.URL.Path == base+"/git/tags":
					phase = "create_tag_object"
				case r.Method == http.MethodPost && r.URL.Path == base+"/git/refs":
					phase = "create_tag_ref"
				case r.Method == http.MethodPost && r.URL.Path == base+"/releases":
					phase = "create_draft"
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/assets"):
					phase = "upload_asset"
				case r.Method == http.MethodPatch:
					phase = "publish_release"
				}
				if phase == failedPhase {
					writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "unknown outcome"})
					return
				}
				switch {
				case r.Method == http.MethodGet && r.URL.Path == base+"/immutable-releases":
					writeJSON(w, http.StatusOK, immutableReleasePolicy{Enabled: true})
				case r.Method == http.MethodGet && (strings.Contains(r.URL.Path, "/releases/tags/") || strings.Contains(r.URL.Path, "/git/ref/tags/")) && len(uploaded) == 0:
					writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
				case phase == "create_tag_object":
					writeJSON(w, http.StatusCreated, annotatedFixture(plan, tagSHA))
				case phase == "create_tag_ref":
					writeJSON(w, http.StatusCreated, annotatedRefFixture(plan, tagSHA))
				case r.Method == http.MethodPost && r.URL.Path == base+"/releases":
					writeJSON(w, http.StatusCreated, publicationReleaseFixture(serverURL(r), plan, true, false))
				case phase == "upload_asset":
					asset := findAsset(t, plan.DraftPlan, r.URL.Query().Get("name"))
					remote := assetFixture(asset, int64(len(uploaded)+1))
					uploaded[asset.Name] = remote
					writeJSON(w, http.StatusCreated, remote)
				case r.Method == http.MethodGet && r.URL.Path == base+"/releases/41":
					writeJSON(w, http.StatusOK, publicationReleaseFixture(serverURL(r), plan, true, false))
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/assets"):
					result := []assetResponse{}
					for _, asset := range plan.Assets {
						result = append(result, uploaded[asset.Name])
					}
					writeJSON(w, http.StatusOK, result)
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/ref/tags/"):
					writeJSON(w, http.StatusOK, annotatedRefFixture(plan, tagSHA))
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/tags/"):
					writeJSON(w, http.StatusOK, annotatedFixture(plan, tagSHA))
				default:
					t.Error("unexpected request", r.Method, r.URL.Path)
				}
			}))
			defer server.Close()
			evidence, err := newTestPublisher(t, server).PublishRelease(t.Context(), plan)
			if err == nil || evidence.State != "uncertain" || evidence.RetryAllowed || evidence.Phase != failedPhase {
				t.Fatal("ambiguous mutation was retryable", evidence, err)
			}
			journal, readErr := os.ReadFile(plan.JournalPath)
			if readErr != nil || !strings.Contains(string(journal), `"phase":"`+failedPhase+`"`) ||
				!strings.Contains(string(journal), `"outcome":"uncertain"`) {
				t.Fatal("uncertain mutation not durable", string(journal), readErr)
			}
		})
	}
}

func publicationPlan(t *testing.T) PublicationPlan {
	t.Helper()
	return PublicationPlan{
		DraftPlan: testPlan(), TagMessage: "DarwinRouter release v1.2.3-rc.1",
		Tagger:     Tagger{Name: "DarwinRouter Release", Email: "release@example.invalid", Date: "2026-09-07T00:01:00Z"},
		MakeLatest: false, JournalPath: filepath.Join(t.TempDir(), "publication.jsonl"),
	}
}

func annotatedFixture(plan PublicationPlan, sha string) annotatedTagResponse {
	response := annotatedTagResponse{SHA: sha, Tag: plan.Tag, Message: plan.TagMessage, Tagger: plan.Tagger}
	response.Object.SHA, response.Object.Type = plan.Commit, "commit"
	return response
}

func annotatedRefFixture(plan PublicationPlan, sha string) tagResponse {
	response := tagResponse{Ref: "refs/tags/" + plan.Tag}
	response.Object.SHA, response.Object.Type = sha, "tag"
	return response
}

func publicationReleaseFixture(origin string, plan PublicationPlan, draft, immutable bool) releaseResponse {
	return releaseResponse{
		ID: 41, TagName: plan.Tag, TargetCommitish: "main", Name: plan.Name, Body: plan.Body,
		Draft: draft, Prerelease: plan.Prerelease, Immutable: immutable,
		UploadURL: origin + "/repos/" + plan.Owner + "/" + plan.Repository + "/releases/41/assets{?name,label}",
	}
}

func TestPublicationPlanCanonicalJSONFields(t *testing.T) {
	plan := publicationPlan(t)
	body, err := json.Marshal(journalExpected(plan))
	if err != nil || !strings.Contains(string(body), `"tag_message"`) || !strings.Contains(string(body), `"content_type"`) {
		t.Fatal("journal authority fields missing", string(body), err)
	}
}

func TestPublicationPlanRejectsUnauthorizedTagMessage(t *testing.T) {
	plan := publicationPlan(t)
	plan.TagMessage = "different message"
	if _, err := validatePublicationPlan(plan); !errors.Is(err, ErrPublish) {
		t.Fatal("non-authorized tag message accepted", err)
	}
}

func TestFinalVerificationEnforcesLatestPolicy(t *testing.T) {
	for _, test := range []struct {
		name       string
		makeLatest bool
		latestID   int64
		wantError  bool
	}{
		{name: "authorized_latest", makeLatest: true, latestID: 41},
		{name: "latest_not_applied", makeLatest: true, latestID: 42, wantError: true},
		{name: "unexpected_latest", latestID: 41, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := publicationPlan(t)
			plan.Tag, plan.Name, plan.TagMessage, plan.Prerelease = "v1.2.3", "DarwinRouter v1.2.3", "DarwinRouter release v1.2.3", false
			plan.MakeLatest = test.makeLatest
			tagSHA := strings.Repeat("c", 40)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				base := "/repos/acme/darwin"
				switch {
				case r.URL.Path == base+"/releases/41":
					writeJSON(w, http.StatusOK, publicationReleaseFixture(serverURL(r), plan, false, true))
				case r.URL.Path == base+"/releases/41/assets":
					assets := make([]assetResponse, len(plan.Assets))
					for i, asset := range plan.Assets {
						assets[i] = assetFixture(asset, int64(i+1))
					}
					writeJSON(w, http.StatusOK, assets)
				case r.URL.Path == base+"/git/ref/tags/"+plan.Tag:
					writeJSON(w, http.StatusOK, annotatedRefFixture(plan, tagSHA))
				case r.URL.Path == base+"/git/tags/"+tagSHA:
					writeJSON(w, http.StatusOK, annotatedFixture(plan, tagSHA))
				case r.URL.Path == base+"/releases/latest":
					latest := publicationReleaseFixture(serverURL(r), plan, false, true)
					latest.ID = test.latestID
					if test.latestID != 41 {
						latest.TagName = "v1.1.0"
					}
					writeJSON(w, http.StatusOK, latest)
				default:
					t.Error("unexpected request", r.URL.Path)
				}
			}))
			defer server.Close()
			publisher := newTestPublisher(t, server)
			evidence := Evidence{}
			err := publisher.verifyPublicationState(t.Context(), &evidence, "/repos/acme/darwin", plan, 41, tagSHA, false, true)
			if (err != nil) != test.wantError {
				t.Fatal("latest policy verification mismatch", err)
			}
		})
	}
}
