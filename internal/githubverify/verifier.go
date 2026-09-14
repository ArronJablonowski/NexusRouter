// Package githubverify performs read-only, exact-byte GitHub release retrieval.
// It has no credential, tag, upload, release-creation, update, or delete path.
package githubverify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const APIVersion = "2026-03-10"

var ErrVerify = errors.New("GitHub release verification failed")

var (
	nameRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,199}$`)
	commitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type RoundTripper interface {
	RoundTrip(*http.Request) (*http.Response, error)
}

type Config struct {
	APIBase                    string
	Transport                  RoundTripper
	Now                        func() time.Time
	ReleaseAttestationVerifier ReleaseAttestationVerifier
}

type ExpectedAsset struct {
	Name        string
	Size        int64
	SHA256      string
	ContentType string
}

type Plan struct {
	Repository     string
	Tag            string
	Commit         string
	TagMessage     string
	Tagger         Tagger
	Title          string
	Body           []byte
	Prerelease     bool
	Assets         []ExpectedAsset
	DownloadDir    string
	ForbiddenRoots []string
}

type Tagger struct {
	Name  string
	Email string
	Date  string
}

type Observation struct {
	Repository         string
	ReleaseID          int64
	ReleaseURL         string
	Tag                string
	Commit             string
	TagObjectSHA       string
	TagMessage         string
	Tagger             Tagger
	Title              string
	BodySHA256         string
	Prerelease         bool
	Immutable          bool
	PublishedAt        string
	ObservedAt         string
	Assets             []ObservedAsset
	DownloadDir        string
	ReleaseAttestation ReleaseAttestationEvidence
}

type ObservedAsset struct {
	ID           int64
	Name         string
	Size         int64
	ServerSHA256 string
	LocalSHA256  string
	ContentType  string
	DownloadURL  string
}

type Verifier struct {
	api         *url.URL
	metadata    *http.Client
	download    *http.Client
	now         func() time.Time
	loopback    bool
	attestation ReleaseAttestationVerifier
}

func New(config Config) (*Verifier, error) {
	if config.Transport == nil || config.Now == nil || config.ReleaseAttestationVerifier == nil {
		return nil, ErrVerify
	}
	api, loopback, err := endpoint(config.APIBase)
	if err != nil {
		return nil, err
	}
	metadata := &http.Client{Transport: config.Transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	verifier := &Verifier{api: api, metadata: metadata, now: config.Now, loopback: loopback, attestation: config.ReleaseAttestationVerifier}
	verifier.download = &http.Client{Transport: config.Transport, Timeout: 2 * time.Minute, CheckRedirect: verifier.checkRedirect}
	return verifier, nil
}

func endpoint(raw string) (*url.URL, bool, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, false, ErrVerify
	}
	host := u.Hostname()
	loopback := host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
	if loopback {
		if u.Scheme != "http" {
			return nil, false, ErrVerify
		}
	} else if u.Scheme != "https" || !strings.EqualFold(host, "api.github.com") || u.Port() != "" && u.Port() != "443" {
		return nil, false, ErrVerify
	}
	u.Path = ""
	return u, loopback, nil
}

func (v *Verifier) Verify(ctx context.Context, input Plan) (Observation, error) {
	var empty Observation
	plan, err := validatePlan(input)
	if err != nil || ctx == nil || ctx.Err() != nil {
		return empty, ErrVerify
	}
	owner, repository, _ := strings.Cut(plan.Repository, "/")
	base := "/repos/" + owner + "/" + repository
	var release releaseResponse
	if err = v.getJSON(ctx, base+"/releases/tags/"+url.PathEscape(plan.Tag), &release); err != nil || !exactRelease(release, plan) {
		return empty, ErrVerify
	}
	var ref refResponse
	if err = v.getJSON(ctx, base+"/git/ref/tags/"+url.PathEscape(plan.Tag), &ref); err != nil || !exactAnnotatedRef(ref, plan) {
		return empty, ErrVerify
	}
	var annotated annotatedTagResponse
	if err = v.getJSON(ctx, base+"/git/tags/"+ref.Object.SHA, &annotated); err != nil ||
		!exactAnnotatedTag(annotated, ref.Object.SHA, plan) {
		return empty, ErrVerify
	}
	remote, err := exactAssets(release.Assets, plan.Assets, plan.Repository, plan.Tag)
	if err != nil {
		return empty, err
	}
	attestation, err := v.attestation.Verify(ctx, ReleaseAttestationPlan{
		Repository: plan.Repository, Tag: plan.Tag, ReleaseID: release.ID,
		TagObjectSHA: annotated.SHA, Assets: plan.Assets,
	})
	if err != nil || !validReleaseAttestationEvidence(attestation, annotated.SHA) {
		return empty, ErrVerify
	}
	root, err := createDownloadRoot(plan.DownloadDir, plan.ForbiddenRoots)
	if err != nil {
		return empty, err
	}
	defer root.Close()
	observed := make([]ObservedAsset, 0, len(remote))
	for i, asset := range remote {
		item, downloadErr := v.downloadAsset(ctx, root, asset, plan.Assets[i])
		if downloadErr != nil {
			return empty, downloadErr
		}
		observed = append(observed, item)
	}
	directory, err := root.Open(".")
	if err != nil {
		return empty, ErrVerify
	}
	syncErr, closeErr := directory.Sync(), directory.Close()
	if syncErr != nil || closeErr != nil || ctx.Err() != nil {
		return empty, ErrVerify
	}
	return Observation{
		Repository: plan.Repository, ReleaseID: release.ID, ReleaseURL: release.HTMLURL,
		Tag: plan.Tag, Commit: plan.Commit, TagObjectSHA: annotated.SHA, TagMessage: annotated.Message, Tagger: annotated.Tagger,
		Title: plan.Title, BodySHA256: digest(plan.Body),
		Prerelease: plan.Prerelease, Immutable: true, PublishedAt: release.PublishedAt,
		ObservedAt: v.now().UTC().Format("2006-01-02T15:04:05Z"), Assets: observed, DownloadDir: plan.DownloadDir,
		ReleaseAttestation: attestation,
	}, nil
}

func validatePlan(input Plan) (Plan, error) {
	owner, repository, ok := strings.Cut(input.Repository, "/")
	if !ok || !nameRE.MatchString(owner) || !nameRE.MatchString(repository) || !nameRE.MatchString(input.Tag) ||
		!commitRE.MatchString(input.Commit) || input.TagMessage == "" || len(input.TagMessage) > 4096 || !validTagger(input.Tagger) ||
		input.Title == "" || len(input.Title) > 256 || len(input.Body) == 0 || len(input.Body) > 1<<20 ||
		len(input.Assets) != 7 || input.DownloadDir == "" {
		return Plan{}, ErrVerify
	}
	result := input
	result.Body = bytes.Clone(input.Body)
	result.Assets = append([]ExpectedAsset(nil), input.Assets...)
	result.ForbiddenRoots = append([]string(nil), input.ForbiddenRoots...)
	previous := ""
	var total int64
	for _, asset := range result.Assets {
		if !nameRE.MatchString(asset.Name) || asset.Name <= previous || asset.Size < 1 || asset.Size > 256<<20 ||
			!digestRE.MatchString(asset.SHA256) || !validContentType(asset.ContentType) || total > (1<<30)-asset.Size {
			return Plan{}, ErrVerify
		}
		previous, total = asset.Name, total+asset.Size
	}
	return result, nil
}

type releaseResponse struct {
	ID              int64           `json:"id"`
	HTMLURL         string          `json:"html_url"`
	TagName         string          `json:"tag_name"`
	TargetCommitish string          `json:"target_commitish"`
	Name            string          `json:"name"`
	Body            string          `json:"body"`
	Draft           bool            `json:"draft"`
	Prerelease      bool            `json:"prerelease"`
	Immutable       bool            `json:"immutable"`
	PublishedAt     string          `json:"published_at"`
	Assets          []assetResponse `json:"assets"`
}

type assetResponse struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	State              string `json:"state"`
	Size               int64  `json:"size"`
	Digest             string `json:"digest"`
	ContentType        string `json:"content_type"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type refResponse struct {
	Ref    string `json:"ref"`
	Object struct {
		Type string `json:"type"`
		SHA  string `json:"sha"`
	} `json:"object"`
}

type annotatedTagResponse struct {
	Tag     string `json:"tag"`
	SHA     string `json:"sha"`
	Message string `json:"message"`
	Tagger  Tagger `json:"tagger"`
	Object  struct {
		Type string `json:"type"`
		SHA  string `json:"sha"`
	} `json:"object"`
}

func exactAnnotatedRef(got refResponse, plan Plan) bool {
	return got.Ref == "refs/tags/"+plan.Tag && got.Object.Type == "tag" && commitRE.MatchString(got.Object.SHA)
}

func exactAnnotatedTag(got annotatedTagResponse, tagObjectSHA string, plan Plan) bool {
	return got.Tag == plan.Tag && got.SHA == tagObjectSHA && got.Message == plan.TagMessage && got.Tagger == plan.Tagger &&
		got.Object.Type == "commit" && got.Object.SHA == plan.Commit
}

func validTagger(tagger Tagger) bool {
	when, err := time.Parse("2006-01-02T15:04:05Z", tagger.Date)
	return err == nil && when.Format("2006-01-02T15:04:05Z") == tagger.Date && len(tagger.Name) > 0 && len(tagger.Name) <= 200 &&
		len(tagger.Email) >= 3 && len(tagger.Email) <= 254 && strings.Contains(tagger.Email, "@") &&
		!strings.ContainsAny(tagger.Name+tagger.Email, "\r\n\x00")
}

func exactRelease(got releaseResponse, want Plan) bool {
	when, err := time.Parse("2006-01-02T15:04:05Z", got.PublishedAt)
	return err == nil && when.Format("2006-01-02T15:04:05Z") == got.PublishedAt && got.ID > 0 && got.Immutable &&
		!got.Draft && got.TagName == want.Tag && got.Name == want.Title &&
		got.Body == string(want.Body) && got.Prerelease == want.Prerelease && len(got.Assets) == len(want.Assets) && validReleaseURL(got.HTMLURL, want)
}

func validReleaseURL(raw string, plan Plan) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if u.Scheme == "http" && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()) != nil && net.ParseIP(u.Hostname()).IsLoopback()) {
		return true
	}
	return u.Scheme == "https" && strings.EqualFold(u.Hostname(), "github.com") && u.Port() == "" &&
		u.EscapedPath() == "/"+plan.Repository+"/releases/tag/"+url.PathEscape(plan.Tag)
}

func exactAssets(got []assetResponse, want []ExpectedAsset, repository, tag string) ([]assetResponse, error) {
	if len(got) != len(want) {
		return nil, ErrVerify
	}
	ordered := append([]assetResponse(nil), got...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	ids := make(map[int64]bool, len(ordered))
	for i := range want {
		asset := ordered[i]
		if asset.ID <= 0 || ids[asset.ID] || asset.Name != want[i].Name || asset.State != "uploaded" || asset.Size != want[i].Size ||
			asset.Digest != want[i].SHA256 || asset.ContentType != want[i].ContentType ||
			!validInitialDownloadURL(asset.BrowserDownloadURL, repository, tag, asset.Name) {
			return nil, ErrVerify
		}
		ids[asset.ID] = true
	}
	return ordered, nil
}

func validContentType(value string) bool {
	return value == "application/gzip" || value == "application/octet-stream"
}

func validInitialDownloadURL(raw, repository, tag, name string) bool {
	u, err := url.Parse(raw)
	wantPath := "/" + repository + "/releases/download/" + url.PathEscape(tag) + "/" + url.PathEscape(name)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.EscapedPath() != wantPath {
		return false
	}
	host := u.Hostname()
	if u.Scheme == "http" && (host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
		return true
	}
	return u.Scheme == "https" && strings.EqualFold(host, "github.com") && u.Port() == ""
}

func (v *Verifier) getJSON(ctx context.Context, requestPath string, output any) error {
	u := *v.api
	u.Path = requestPath
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return ErrVerify
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", APIVersion)
	request.Header.Set("User-Agent", "DarwinRouter-release-verifier/1")
	response, err := v.metadata.Do(request)
	if err != nil {
		return ErrVerify
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > 1<<20 {
		return ErrVerify
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, (1<<20)+1))
	if decoder.Decode(output) != nil {
		return ErrVerify
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return ErrVerify
	}
	return nil
}

func (v *Verifier) downloadAsset(ctx context.Context, root *os.Root, remote assetResponse, expected ExpectedAsset) (ObservedAsset, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, remote.BrowserDownloadURL, nil)
	if err != nil {
		return ObservedAsset{}, ErrVerify
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("User-Agent", "DarwinRouter-release-verifier/1")
	response, err := v.download.Do(request)
	if err != nil {
		return ObservedAsset{}, ErrVerify
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength != expected.Size || response.Header.Get("Content-Type") != expected.ContentType {
		return ObservedAsset{}, ErrVerify
	}
	file, err := root.OpenFile(expected.Name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return ObservedAsset{}, ErrVerify
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, expected.Size+1))
	syncErr, closeErr := file.Sync(), file.Close()
	local := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if copyErr != nil || syncErr != nil || closeErr != nil || n != expected.Size || local != expected.SHA256 || local != remote.Digest {
		return ObservedAsset{}, ErrVerify
	}
	return ObservedAsset{ID: remote.ID, Name: expected.Name, Size: n, ServerSHA256: remote.Digest, LocalSHA256: local, ContentType: remote.ContentType, DownloadURL: remote.BrowserDownloadURL}, nil
}

func (v *Verifier) checkRedirect(request *http.Request, via []*http.Request) error {
	if len(via) > 3 || request.Method != http.MethodGet || request.URL.User != nil ||
		(request.URL.RawQuery != "" && len(request.URL.RawQuery) > 4096) || request.URL.Fragment != "" {
		return ErrVerify
	}
	host := request.URL.Hostname()
	if v.loopback && request.URL.Scheme == "http" && (host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
		return nil
	}
	if request.URL.Scheme != "https" || request.URL.Port() != "" && request.URL.Port() != "443" {
		return ErrVerify
	}
	for _, allowed := range []string{"github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com"} {
		if strings.EqualFold(host, allowed) {
			request.Header.Del("Authorization")
			return nil
		}
	}
	return ErrVerify
}

func createDownloadRoot(out string, forbidden []string) (*os.Root, error) {
	abs, err := filepath.Abs(out)
	if err != nil || filepath.Base(abs) == "." || filepath.Base(abs) == string(filepath.Separator) {
		return nil, ErrVerify
	}
	parent := filepath.Dir(abs)
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrVerify
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return nil, ErrVerify
	}
	abs = filepath.Join(realParent, filepath.Base(abs))
	for _, root := range forbidden {
		real, evalErr := filepath.EvalSymlinks(root)
		if evalErr != nil {
			return nil, ErrVerify
		}
		rel, relErr := filepath.Rel(real, abs)
		if relErr != nil || rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, ErrVerify
		}
	}
	if err = os.Mkdir(abs, 0700); err != nil {
		return nil, ErrVerify
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, ErrVerify
	}
	return root, nil
}

func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}
