package githubpublish

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCreateOnlyDraftSuccess(t *testing.T) {
	plan := testPlan()
	var mu sync.Mutex
	createdTag, createdRelease := false, false
	uploaded := map[string]assetResponse{}
	methods := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		methods = append(methods, r.Method+" "+r.URL.EscapedPath())
		assertHeaders(t, r)
		if r.Header.Get("Authorization") != "" {
			t.Error("publisher added credentials")
		}
		base := "/repos/acme/darwin"
		switch {
		case r.Method == http.MethodGet && r.URL.Path == base+"/git/ref/tags/"+plan.Tag:
			if !createdTag {
				writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
				return
			}
			writeJSON(w, http.StatusOK, tagFixture(plan))
		case r.Method == http.MethodGet && r.URL.Path == base+"/releases/tags/"+plan.Tag:
			if createdRelease {
				t.Error("release absence checked after creation")
			}
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		case r.Method == http.MethodPost && r.URL.Path == base+"/git/refs":
			var input struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			}
			decodeRequest(t, r, &input)
			if createdTag || input.Ref != "refs/tags/"+plan.Tag || input.SHA != plan.Commit {
				t.Error("non-create-only or inexact tag request")
			}
			createdTag = true
			writeJSON(w, http.StatusCreated, tagFixture(plan))
		case r.Method == http.MethodPost && r.URL.Path == base+"/releases":
			var input createReleaseRequest
			decodeRequest(t, r, &input)
			want := createReleaseRequest{TagName: plan.Tag, TargetCommitish: plan.Commit, Name: plan.Name, Body: plan.Body, Draft: true, Prerelease: true, GenerateReleaseNotes: false}
			if !createdTag || createdRelease || input != want {
				t.Error("inexact draft request", input)
			}
			createdRelease = true
			writeJSON(w, http.StatusCreated, releaseFixture(serverURL(r), plan))
		case r.Method == http.MethodPost && r.URL.Path == base+"/releases/41/assets":
			name := r.URL.Query().Get("name")
			if len(r.URL.Query()) != 1 || name == "" || !createdRelease {
				t.Error("invalid asset request")
			}
			asset := findAsset(t, plan, name)
			body, err := io.ReadAll(r.Body)
			if err != nil || string(body) != string(asset.Body) || r.ContentLength != int64(len(asset.Body)) || r.Header.Get("Content-Type") != asset.ContentType {
				t.Error("asset bytes or metadata changed", err)
			}
			remote := assetFixture(asset, int64(len(uploaded)+1))
			uploaded[name] = remote
			writeJSON(w, http.StatusCreated, remote)
		case r.Method == http.MethodGet && r.URL.Path == base+"/releases/41":
			writeJSON(w, http.StatusOK, releaseFixture(serverURL(r), plan))
		case r.Method == http.MethodGet && r.URL.Path == base+"/releases/41/assets":
			if r.URL.Query().Get("page") != "1" || r.URL.Query().Get("per_page") != "100" || len(r.URL.Query()) != 2 {
				t.Error("asset inspection is not bounded")
			}
			result := make([]assetResponse, 0, len(uploaded))
			for _, asset := range plan.Assets {
				result = append(result, uploaded[asset.Name])
			}
			writeJSON(w, http.StatusOK, result)
		default:
			t.Errorf("unexpected method/path: %s %s", r.Method, r.URL.String())
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "rejected"})
		}
	}))
	defer server.Close()

	publisher := newTestPublisher(t, server)
	evidence, err := publisher.PublishDraft(t.Context(), plan)
	if err != nil || evidence.State != "confirmed_draft" || evidence.Phase != "complete" || evidence.RetryAllowed || !evidence.TagCreated || evidence.ReleaseID != 41 || len(evidence.UploadedAssets) != len(plan.Assets) || evidence.Requests != 7+len(plan.Assets) {
		t.Fatal("draft publication evidence", evidence, err)
	}
	for _, request := range methods {
		if strings.HasPrefix(request, http.MethodPatch+" ") || strings.HasPrefix(request, http.MethodPut+" ") || strings.HasPrefix(request, http.MethodDelete+" ") {
			t.Fatal("update/delete request issued", request)
		}
	}
}

func TestPreexistingRemoteStateStopsBeforeMutation(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusMovedPermanently, http.StatusInternalServerError} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				writeJSON(w, status, tagFixture(testPlan()))
			}))
			defer server.Close()
			evidence, err := newTestPublisher(t, server).PublishDraft(t.Context(), testPlan())
			if err == nil || evidence.State != "not_started" || !evidence.RetryAllowed || evidence.TagCreated || evidence.ReleaseID != 0 || requests.Load() != 1 {
				t.Fatal("preexisting or ambiguous state was mutated", evidence, err, requests.Load())
			}
		})
	}
}

func TestFailureAfterMutationReturnsUncertainEvidence(t *testing.T) {
	plan := testPlan()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		base := "/repos/acme/darwin"
		switch {
		case r.Method == http.MethodGet:
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		case r.URL.Path == base+"/git/refs":
			writeJSON(w, http.StatusCreated, tagFixture(plan))
		case r.URL.Path == base+"/releases":
			writeJSON(w, http.StatusCreated, map[string]any{"id": 41, "tag_name": plan.Tag, "draft": false})
		default:
			t.Error("unexpected request", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	evidence, err := newTestPublisher(t, server).PublishDraft(t.Context(), plan)
	if err == nil || evidence.State != "uncertain" || evidence.Phase != "create_draft" || evidence.RetryAllowed || !evidence.TagCreated || evidence.ReleaseID != 0 || evidence.Requests != 4 || requests.Load() != 4 {
		t.Fatal("partial mutation was not reported as uncertain", evidence, err)
	}
}

func TestFailedAssetNamesPossiblyCreatedRemoteObject(t *testing.T) {
	plan := testPlan()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch requests.Add(1) {
		case 1, 2:
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		case 3:
			writeJSON(w, http.StatusCreated, tagFixture(plan))
		case 4:
			writeJSON(w, http.StatusCreated, releaseFixture(serverURL(r), plan))
		case 5:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "unknown outcome"})
		default:
			t.Error("failed asset was retried")
		}
	}))
	defer server.Close()
	evidence, err := newTestPublisher(t, server).PublishDraft(t.Context(), plan)
	if err == nil || evidence.State != "uncertain" || evidence.RetryAllowed || evidence.Phase != "upload_asset" || evidence.PendingAsset != plan.Assets[0].Name || evidence.ReleaseID != 41 || len(evidence.UploadedAssets) != 0 || requests.Load() != 5 {
		t.Fatal("uncertain asset state was not retained", evidence, err)
	}
}

func TestCancellationDuringFirstMutationIsUncertainAndNotRetried(t *testing.T) {
	plan := testPlan()
	var requests atomic.Int32
	mutating := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
			return
		}
		close(mutating)
		time.Sleep(200 * time.Millisecond)
		writeJSON(w, http.StatusCreated, tagFixture(plan))
	}))
	defer server.Close()
	publisher := newTestPublisher(t, server)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var evidence Evidence
	var err error
	go func() {
		evidence, err = publisher.PublishDraft(ctx, plan)
		close(done)
	}()
	select {
	case <-mutating:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("mutation did not start")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation was not bounded")
	}
	if err == nil || evidence.State != "uncertain" || evidence.Phase != "create_tag" || evidence.RetryAllowed || evidence.Requests != 3 || requests.Load() != 3 {
		t.Fatal("canceled mutation was retried or reported as absent", evidence, err, requests.Load())
	}
}

func TestRedirectAndResponseBoundsFailClosed(t *testing.T) {
	var redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer destination.Close()
	for name, handler := range map[string]http.HandlerFunc{
		"redirect": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, destination.URL, http.StatusFound)
		},
		"oversize": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, strings.Repeat("x", maxResponseBytes+1))
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(handler)
			defer server.Close()
			evidence, err := newTestPublisher(t, server).PublishDraft(t.Context(), testPlan())
			if err == nil || evidence.State != "not_started" || evidence.Requests != 1 {
				t.Fatal("redirect/oversize response accepted", evidence, err)
			}
		})
	}
	if redirected.Load() != 0 {
		t.Fatal("redirect destination contacted")
	}
}

func TestPlanAndOriginValidationPrecedesRequests(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	publisher := newTestPublisher(t, server)
	for name, mutate := range map[string]func(*DraftPlan){
		"owner":       func(p *DraftPlan) { p.Owner = "../owner" },
		"repository":  func(p *DraftPlan) { p.Repository = "repo/path" },
		"tag":         func(p *DraftPlan) { p.Tag = "latest" },
		"tag_semver":  func(p *DraftPlan) { p.Tag = "v1.2.3-01" },
		"commit":      func(p *DraftPlan) { p.Commit = strings.Repeat("A", 40) },
		"authority":   func(p *DraftPlan) { p.AuthorizationSHA256 = "bad" },
		"notes":       func(p *DraftPlan) { p.ReleaseNotesSHA256 = "sha256:" + strings.Repeat("0", 64) },
		"title":       func(p *DraftPlan) { p.Name = "Different title" },
		"prerelease":  func(p *DraftPlan) { p.Prerelease = false },
		"not_draft":   func(p *DraftPlan) { p.Draft = false },
		"empty_body":  func(p *DraftPlan) { p.Body = "" },
		"asset_order": func(p *DraftPlan) { p.Assets[0], p.Assets[1] = p.Assets[1], p.Assets[0] },
		"asset_digest": func(p *DraftPlan) {
			p.Assets[0].SHA256 = "sha256:" + strings.Repeat("0", 64)
		},
		"asset_name":  func(p *DraftPlan) { p.Assets[0].Name = "../asset" },
		"asset_slash": func(p *DraftPlan) { p.Assets[0].Name = `bad\asset` },
		"media":       func(p *DraftPlan) { p.Assets[0].ContentType = "bad" },
	} {
		t.Run(name, func(t *testing.T) {
			plan := testPlan()
			mutate(&plan)
			evidence, err := publisher.PublishDraft(t.Context(), plan)
			if err == nil || evidence.State != "not_started" || evidence.Requests != 0 {
				t.Fatal("invalid plan reached transport", evidence, err)
			}
		})
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if evidence, err := publisher.PublishDraft(canceled, testPlan()); err == nil || evidence.Requests != 0 || evidence.State != "not_started" {
		t.Fatal("pre-canceled operation reached transport", evidence, err)
	}
	if requests.Load() != 0 {
		t.Fatal("invalid input made HTTP requests", requests.Load())
	}

	for _, config := range []Config{
		{APIBase: "http://github.com", UploadBase: server.URL, Transport: server.Client().Transport},
		{APIBase: server.URL + "/prefix", UploadBase: server.URL, Transport: server.Client().Transport},
		{APIBase: server.URL, UploadBase: "https://user@example.com", Transport: server.Client().Transport},
		{APIBase: "https://example.com", UploadBase: "https://uploads.github.com", Transport: server.Client().Transport},
		{APIBase: server.URL, UploadBase: server.URL},
	} {
		if _, err := New(config); err == nil {
			t.Fatal("unsafe origin/transport accepted", config.APIBase, config.UploadBase)
		}
	}
}

func testPlan() DraftPlan {
	assets := []Asset{
		{Name: "NexusRouter_1.2.3-rc.1_darwin_arm64.tar.gz", ContentType: "application/gzip", Body: []byte("darwin-archive")},
		{Name: "SHA256SUMS", ContentType: "text/plain", Body: []byte("signed-checksums\n")},
	}
	for i := range assets {
		assets[i].SHA256 = digest(assets[i].Body)
	}
	body := "Exact release notes.\n"
	return DraftPlan{AuthorizationSHA256: "sha256:" + strings.Repeat("b", 64), ReleaseNotesSHA256: digest([]byte(body)), Owner: "acme", Repository: "darwin", Tag: "v1.2.3-rc.1", Commit: strings.Repeat("a", 40), Name: "NexusRouter v1.2.3-rc.1", Body: body, Draft: true, Prerelease: true, Assets: assets}
}

func newTestPublisher(t *testing.T, server *httptest.Server) *Publisher {
	t.Helper()
	publisher, err := New(Config{APIBase: server.URL, UploadBase: server.URL, Transport: server.Client().Transport})
	if err != nil {
		t.Fatal(err)
	}
	return publisher
}

func tagFixture(plan DraftPlan) tagResponse {
	response := tagResponse{Ref: "refs/tags/" + plan.Tag}
	response.Object.SHA, response.Object.Type = plan.Commit, "commit"
	return response
}

func releaseFixture(origin string, plan DraftPlan) releaseResponse {
	return releaseResponse{ID: 41, TagName: plan.Tag, Name: plan.Name, Body: plan.Body, Draft: true, Prerelease: plan.Prerelease, UploadURL: origin + "/repos/" + plan.Owner + "/" + plan.Repository + "/releases/41/assets{?name,label}"}
}

func assetFixture(asset Asset, id int64) assetResponse {
	return assetResponse{ID: id, Name: asset.Name, Size: int64(len(asset.Body)), ContentType: asset.ContentType, State: "uploaded", Digest: asset.SHA256}
}

func serverURL(r *http.Request) string { return "http://" + r.Host }

func findAsset(t *testing.T, plan DraftPlan, name string) Asset {
	t.Helper()
	for _, asset := range plan.Assets {
		if asset.Name == name {
			return asset
		}
	}
	t.Fatal("unknown asset", name)
	return Asset{}
}

func assertHeaders(t *testing.T, request *http.Request) {
	t.Helper()
	if request.Header.Get("X-GitHub-Api-Version") != APIVersion || request.Header.Get("Accept") != "application/vnd.github+json" || request.Header.Get("User-Agent") != "NexusRouter-create-only-publisher/1" {
		t.Error("unpinned GitHub request headers")
	}
}

func decodeRequest(t *testing.T, request *http.Request, destination any) {
	t.Helper()
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxResponseBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		t.Fatal(err)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func TestEndpointAllowsOnlyHTTPSOrLoopbackHTTP(t *testing.T) {
	for _, value := range []string{"https://api.github.com", "http://127.0.0.1:1234", "http://[::1]:1234"} {
		if _, err := endpoint(value, "api.github.com"); err != nil {
			t.Fatal("safe endpoint rejected", value, err)
		}
	}
	for _, value := range []string{"", "http://api.github.com", "ftp://api.github.com", "https://example.com", "https://api.github.com/path", "https://api.github.com?q=x", "https://user@api.github.com"} {
		if _, err := endpoint(value, "api.github.com"); err == nil {
			t.Fatal("unsafe endpoint accepted", value)
		}
	}
}

func TestUploadURLCannotRedirectToAnotherHost(t *testing.T) {
	plan := testPlan()
	other, _ := url.Parse("https://uploads.example.invalid")
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		switch count.Load() {
		case 1, 2:
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		case 3:
			writeJSON(w, http.StatusCreated, tagFixture(plan))
		default:
			response := releaseFixture(serverURL(r), plan)
			response.UploadURL = other.String() + "/assets{?name,label}"
			writeJSON(w, http.StatusCreated, response)
		}
	}))
	defer server.Close()
	evidence, err := newTestPublisher(t, server).PublishDraft(t.Context(), plan)
	if err == nil || evidence.State != "uncertain" || evidence.Phase != "create_draft" || evidence.Requests != 4 {
		t.Fatal("foreign upload host accepted", evidence, err)
	}
}
