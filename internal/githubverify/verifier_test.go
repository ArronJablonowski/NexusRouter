package githubverify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadOnlyImmutableReleaseDownload(t *testing.T) {
	contents := map[string][]byte{}
	assets := make([]ExpectedAsset, 0, 7)
	for i, name := range []string{"DarwinRouter_1.0.0_darwin_amd64.tar.gz", "DarwinRouter_1.0.0_darwin_arm64.tar.gz", "DarwinRouter_1.0.0_linux_amd64.tar.gz", "DarwinRouter_1.0.0_linux_arm64.tar.gz", "SHA256SUMS", "SHA256SUMS.sig", "manifest.json"} {
		body := []byte(fmt.Sprintf("asset-%d\n", i))
		contents[name] = body
		contentType := "application/octet-stream"
		if strings.HasSuffix(name, ".tar.gz") {
			contentType = "application/gzip"
		}
		assets = append(assets, ExpectedAsset{Name: name, Size: int64(len(body)), SHA256: digest(body), ContentType: contentType})
	}
	commit := strings.Repeat("a", 40)
	tagObjectSHA := strings.Repeat("b", 40)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
			t.Error("non-read-only or credentialed request", r.Method)
		}
		switch r.URL.Path {
		case "/repos/acme/router/releases/tags/v1.0.0":
			remote := make([]assetResponse, len(assets))
			for i, asset := range assets {
				remote[i] = assetResponse{ID: int64(i + 1), Name: asset.Name, State: "uploaded", Size: asset.Size, Digest: asset.SHA256, ContentType: asset.ContentType, BrowserDownloadURL: server.URL + "/download/" + asset.Name}
			}
			writeJSON(t, w, releaseResponse{ID: 41, HTMLURL: "https://github.com/acme/router/releases/tag/v1.0.0", TagName: "v1.0.0", TargetCommitish: commit, Name: "DarwinRouter v1.0.0", Body: "notes\n", Immutable: true, PublishedAt: "2026-09-07T01:00:00Z", Assets: remote})
		case "/repos/acme/router/git/ref/tags/v1.0.0":
			var ref refResponse
			ref.Ref, ref.Object.Type, ref.Object.SHA = "refs/tags/v1.0.0", "tag", tagObjectSHA
			writeJSON(t, w, ref)
		case "/repos/acme/router/git/tags/" + tagObjectSHA:
			var tag annotatedTagResponse
			tag.Tag, tag.SHA, tag.Object.Type, tag.Object.SHA = "v1.0.0", tagObjectSHA, "commit", commit
			writeJSON(t, w, tag)
		default:
			name := filepath.Base(r.URL.Path)
			body, ok := contents[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			for _, asset := range assets {
				if asset.Name == name {
					w.Header().Set("Content-Type", asset.ContentType)
				}
			}
			_, _ = w.Write(body)
		}
	}))
	defer server.Close()
	verifier, err := New(Config{APIBase: server.URL, Transport: server.Client().Transport, Now: func() time.Time { return time.Date(2026, 9, 7, 1, 1, 0, 999, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "fresh")
	result, err := verifier.Verify(context.Background(), Plan{Repository: "acme/router", Tag: "v1.0.0", Commit: commit, Title: "DarwinRouter v1.0.0", Body: []byte("notes\n"), Assets: assets, DownloadDir: out})
	if err != nil || !result.Immutable || result.ObservedAt != "2026-09-07T01:01:00Z" || len(result.Assets) != 7 {
		t.Fatal("verification failed", result, err)
	}
	for _, asset := range assets {
		body, readErr := os.ReadFile(filepath.Join(out, asset.Name))
		if readErr != nil || digest(body) != asset.SHA256 {
			t.Fatal("fresh asset mismatch", asset.Name, readErr)
		}
	}
}

func TestVerifierRejectsRemoteDriftAndUnsafeOutput(t *testing.T) {
	asset := ExpectedAsset{Name: "only", Size: 1, SHA256: digest([]byte("x")), ContentType: "application/octet-stream"}
	if _, err := validatePlan(Plan{Repository: "a/b", Tag: "v1.0.0", Commit: strings.Repeat("a", 40), Title: "t", Body: []byte("n"), Assets: []ExpectedAsset{asset}, DownloadDir: "out"}); err == nil {
		t.Fatal("wrong asset set accepted")
	}
	parent := t.TempDir()
	forbidden := filepath.Join(parent, "source")
	if err := os.Mkdir(forbidden, 0700); err != nil {
		t.Fatal(err)
	}
	if root, err := createDownloadRoot(filepath.Join(forbidden, "download"), []string{forbidden}); err == nil {
		root.Close()
		t.Fatal("download inside forbidden root accepted")
	}
	if _, _, err := endpoint("https://example.com"); err == nil {
		t.Fatal("non-GitHub API accepted")
	}
}

func TestExactAnnotatedTagRejectsLightweightNestedAndMismatchedStates(t *testing.T) {
	commit, object := strings.Repeat("a", 40), strings.Repeat("b", 40)
	plan := Plan{Tag: "v1.0.0", Commit: commit}
	var valid annotatedTagResponse
	valid.Tag, valid.SHA, valid.Object.Type, valid.Object.SHA = plan.Tag, object, "commit", commit
	if !exactAnnotatedTag(valid, object, plan) {
		t.Fatal("valid annotated tag rejected")
	}
	for name, mutate := range map[string]func(*annotatedTagResponse){
		"wrong_name":   func(tag *annotatedTagResponse) { tag.Tag = "v1.0.1" },
		"wrong_object": func(tag *annotatedTagResponse) { tag.SHA = strings.Repeat("c", 40) },
		"nested_tag":   func(tag *annotatedTagResponse) { tag.Object.Type = "tag" },
		"wrong_commit": func(tag *annotatedTagResponse) { tag.Object.SHA = strings.Repeat("d", 40) },
		"missing":      func(tag *annotatedTagResponse) { tag.Object.SHA = "" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := valid
			mutate(&changed)
			if exactAnnotatedTag(changed, object, plan) {
				t.Fatal("invalid annotated tag accepted")
			}
		})
	}
	ref := refResponse{Ref: "refs/tags/v1.0.0"}
	ref.Object.Type, ref.Object.SHA = "tag", object
	if !exactAnnotatedRef(ref, plan) {
		t.Fatal("annotated ref rejected")
	}
	ref.Object.Type = "commit"
	if exactAnnotatedRef(ref, plan) {
		t.Fatal("lightweight ref accepted")
	}
	ref.Object.Type, ref.Object.SHA = "tag", "short"
	if exactAnnotatedRef(ref, plan) {
		t.Fatal("malformed tag object identity accepted")
	}
}

func TestExactAssetsRejectsContentTypeDrift(t *testing.T) {
	want := []ExpectedAsset{{Name: "asset", Size: 1, SHA256: digest([]byte("x")), ContentType: "application/octet-stream"}}
	got := []assetResponse{{ID: 1, Name: "asset", State: "uploaded", Size: 1, Digest: want[0].SHA256, ContentType: "text/plain", BrowserDownloadURL: "https://github.com/a/b/releases/download/v/asset"}}
	if _, err := exactAssets(got, want); err == nil {
		t.Fatal("remote content-type drift accepted")
	}
}

func TestRedirectPolicyRejectsUntrustedHosts(t *testing.T) {
	verifier, err := New(Config{APIBase: "https://api.github.com", Transport: rejectingTransport{}, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "https://evil.example/asset", nil)
	via := []*http.Request{httptest.NewRequest(http.MethodGet, "https://github.com/a/b/releases/download/v1/x", nil)}
	if verifier.checkRedirect(req, via) == nil {
		t.Fatal("untrusted redirect accepted")
	}
	req = httptest.NewRequest(http.MethodGet, "https://release-assets.githubusercontent.com/x?token=opaque", nil)
	if verifier.checkRedirect(req, via) != nil {
		t.Fatal("approved GitHub asset redirect rejected")
	}
}

func TestExactReleaseRejectsMetadataDrift(t *testing.T) {
	commit := strings.Repeat("a", 40)
	plan := Plan{Repository: "acme/router", Tag: "v1.0.0", Commit: commit, Title: "DarwinRouter v1.0.0", Body: []byte("notes\n")}
	valid := releaseResponse{
		ID: 1, HTMLURL: "https://github.com/acme/router/releases/tag/v1.0.0", TagName: plan.Tag,
		TargetCommitish: commit, Name: plan.Title, Body: string(plan.Body), Immutable: true,
		PublishedAt: "2026-09-07T01:00:00Z",
	}
	for name, mutate := range map[string]func(*releaseResponse){
		"mutable":      func(r *releaseResponse) { r.Immutable = false },
		"draft":        func(r *releaseResponse) { r.Draft = true },
		"tag":          func(r *releaseResponse) { r.TagName = "v1.0.1" },
		"commit":       func(r *releaseResponse) { r.TargetCommitish = strings.Repeat("b", 40) },
		"title":        func(r *releaseResponse) { r.Name += " changed" },
		"body":         func(r *releaseResponse) { r.Body += "changed\n" },
		"prerelease":   func(r *releaseResponse) { r.Prerelease = true },
		"timestamp":    func(r *releaseResponse) { r.PublishedAt = "2026-09-07T01:00:00.1Z" },
		"release_host": func(r *releaseResponse) { r.HTMLURL = "https://evil.example/acme/router/releases/tag/v1.0.0" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := valid
			mutate(&changed)
			if exactRelease(changed, plan) {
				t.Fatal("remote metadata drift accepted")
			}
		})
	}
}

type rejectingTransport struct{}

func (rejectingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("network disabled in test")
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}
