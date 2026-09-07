// Package githubpublish contains a create-only GitHub draft-release client.
// It has no credential handling and is inert until a caller supplies a plan,
// transport, endpoint and context.
package githubpublish

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	APIVersion       = "2022-11-28"
	maxAssets        = 16
	maxAssetBytes    = 256 << 20
	maxTotalBytes    = 512 << 20
	maxResponseBytes = 1 << 20
	maxRequests      = 32
	requestTimeout   = 30 * time.Second
)

var (
	ErrPublish  = errors.New("create-only GitHub draft publication failed")
	nameRE      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
	assetNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,199}$`)
	tagRE       = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)
	commitRE    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestRE    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	mediaRE     = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]{0,63}/[a-z0-9][a-z0-9!#$&^_.+-]{0,63}$`)
)

// RoundTripper is the only I/O seam. Publisher wraps it in an http.Client that
// rejects redirects and imposes per-request deadlines.
type RoundTripper interface {
	RoundTrip(*http.Request) (*http.Response, error)
}

// Config pins both GitHub origins. HTTP is accepted only for loopback test
// servers; every other origin must use HTTPS.
type Config struct {
	APIBase    string
	UploadBase string
	Transport  RoundTripper
}

// DraftPlan is deliberately independent of releasepack's authorization types.
// A later adapter must copy an already-authorized exact plan into this seam.
type DraftPlan struct {
	AuthorizationSHA256 string
	ReleaseNotesSHA256  string
	Owner               string
	Repository          string
	Tag                 string
	Commit              string
	Name                string
	Body                string
	Draft               bool
	Prerelease          bool
	Assets              []Asset
}

// Asset contains exact immutable bytes for one upload. PublishDraft clones all
// bodies and validates their size and digest before its first HTTP request.
type Asset struct {
	Name        string
	ContentType string
	SHA256      string
	Body        []byte
}

// Evidence is returned on both success and failure. "uncertain" means at
// least one mutating request may have reached GitHub and must not be retried.
type Evidence struct {
	SchemaVersion       int             `json:"schema_version"`
	Scope               string          `json:"scope"`
	Repository          string          `json:"repository"`
	AuthorizationSHA256 string          `json:"authorization_sha256"`
	ReleaseNotesSHA256  string          `json:"release_notes_sha256"`
	Tag                 string          `json:"tag"`
	Commit              string          `json:"commit"`
	Draft               bool            `json:"draft"`
	State               string          `json:"state"`
	Phase               string          `json:"phase"`
	RetryAllowed        bool            `json:"retry_allowed"`
	Requests            int             `json:"requests"`
	TagCreated          bool            `json:"tag_created"`
	ReleaseID           int64           `json:"release_id,omitempty"`
	PendingAsset        string          `json:"pending_asset,omitempty"`
	UploadedAssets      []EvidenceAsset `json:"uploaded_assets"`
}

type EvidenceAsset struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Publisher struct {
	api, upload *url.URL
	client      *http.Client
}

func New(config Config) (*Publisher, error) {
	if config.Transport == nil {
		return nil, ErrPublish
	}
	api, err := endpoint(config.APIBase, "api.github.com")
	if err != nil {
		return nil, err
	}
	upload, err := endpoint(config.UploadBase, "uploads.github.com")
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		Transport: config.Transport,
		Timeout:   requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &Publisher{api: api, upload: upload, client: client}, nil
}

func endpoint(value, githubHost string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.User != nil || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, ErrPublish
	}
	host := u.Hostname()
	loopback := host == "localhost" || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
	if (!loopback && (u.Scheme != "https" || !strings.EqualFold(host, githubHost) || (u.Port() != "" && u.Port() != "443"))) || (loopback && u.Scheme != "http") {
		return nil, ErrPublish
	}
	u.Path = ""
	return u, nil
}

func (p *Publisher) PublishDraft(ctx context.Context, input DraftPlan) (Evidence, error) {
	evidence := Evidence{SchemaVersion: 1, Scope: "darwinrouter-github-create-only-draft", State: "not_started", Phase: "validate", RetryAllowed: true, UploadedAssets: []EvidenceAsset{}}
	plan, err := validateAndClone(input)
	if err != nil {
		return evidence, ErrPublish
	}
	evidence.Repository, evidence.AuthorizationSHA256, evidence.ReleaseNotesSHA256 = plan.Owner+"/"+plan.Repository, plan.AuthorizationSHA256, plan.ReleaseNotesSHA256
	evidence.Tag, evidence.Commit, evidence.Draft = plan.Tag, plan.Commit, plan.Draft
	if ctx == nil || ctx.Err() != nil {
		return evidence, ErrPublish
	}

	repositoryPath := "/repos/" + plan.Owner + "/" + plan.Repository
	if err = p.expectAbsent(ctx, &evidence, repositoryPath+"/git/ref/tags/"+url.PathEscape(plan.Tag)); err != nil {
		return evidence, err
	}
	if err = p.expectAbsent(ctx, &evidence, repositoryPath+"/releases/tags/"+url.PathEscape(plan.Tag)); err != nil {
		return evidence, err
	}

	evidence.State, evidence.Phase, evidence.RetryAllowed = "uncertain", "create_tag", false
	var tag tagResponse
	if err = p.jsonRequest(ctx, &evidence, p.api, http.MethodPost, repositoryPath+"/git/refs", nil, struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	}{Ref: "refs/tags/" + plan.Tag, SHA: plan.Commit}, &tag, http.StatusCreated); err != nil || !exactTag(tag, plan) {
		return evidence, ErrPublish
	}
	evidence.TagCreated = true

	evidence.Phase = "create_draft"
	request := createReleaseRequest{TagName: plan.Tag, TargetCommitish: plan.Commit, Name: plan.Name, Body: plan.Body, Draft: true, Prerelease: plan.Prerelease, GenerateReleaseNotes: false}
	var release releaseResponse
	if err = p.jsonRequest(ctx, &evidence, p.api, http.MethodPost, repositoryPath+"/releases", nil, request, &release, http.StatusCreated); err != nil || !exactRelease(release, plan) {
		return evidence, ErrPublish
	}
	evidence.ReleaseID = release.ID
	expectedUpload := p.upload.String() + repositoryPath + "/releases/" + strconv.FormatInt(release.ID, 10) + "/assets{?name,label}"
	if release.UploadURL != expectedUpload {
		return evidence, ErrPublish
	}

	for _, asset := range plan.Assets {
		evidence.Phase, evidence.PendingAsset = "upload_asset", asset.Name
		query := url.Values{"name": []string{asset.Name}}
		var remote assetResponse
		if err = p.assetRequest(ctx, &evidence, repositoryPath+"/releases/"+strconv.FormatInt(release.ID, 10)+"/assets", query, asset, &remote); err != nil || !exactAsset(remote, asset) {
			return evidence, ErrPublish
		}
		evidence.UploadedAssets = append(evidence.UploadedAssets, evidenceAsset(asset, remote.ID))
		evidence.PendingAsset = ""
	}

	evidence.Phase = "verify_release"
	var finalRelease releaseResponse
	if err = p.jsonRequest(ctx, &evidence, p.api, http.MethodGet, repositoryPath+"/releases/"+strconv.FormatInt(release.ID, 10), nil, nil, &finalRelease, http.StatusOK); err != nil || !exactRelease(finalRelease, plan) || finalRelease.ID != release.ID || finalRelease.UploadURL != expectedUpload {
		return evidence, ErrPublish
	}
	evidence.Phase = "verify_assets"
	var assets []assetResponse
	if err = p.jsonRequest(ctx, &evidence, p.api, http.MethodGet, repositoryPath+"/releases/"+strconv.FormatInt(release.ID, 10)+"/assets", url.Values{"page": {"1"}, "per_page": {"100"}}, nil, &assets, http.StatusOK); err != nil || !exactAssets(assets, plan.Assets) {
		return evidence, ErrPublish
	}
	evidence.Phase = "verify_tag"
	var finalTag tagResponse
	if err = p.jsonRequest(ctx, &evidence, p.api, http.MethodGet, repositoryPath+"/git/ref/tags/"+url.PathEscape(plan.Tag), nil, nil, &finalTag, http.StatusOK); err != nil || !exactTag(finalTag, plan) {
		return evidence, ErrPublish
	}
	evidence.State, evidence.Phase = "confirmed_draft", "complete"
	return evidence, nil
}

func validateAndClone(input DraftPlan) (DraftPlan, error) {
	if !digestRE.MatchString(input.AuthorizationSHA256) || !digestRE.MatchString(input.ReleaseNotesSHA256) ||
		!nameRE.MatchString(input.Owner) || !nameRE.MatchString(input.Repository) || !validTag(input.Tag) ||
		!commitRE.MatchString(input.Commit) || !input.Draft || input.Name != "DarwinRouter "+input.Tag ||
		input.Body == "" || len(input.Body) > 1<<20 || digest([]byte(input.Body)) != input.ReleaseNotesSHA256 ||
		input.Prerelease != strings.Contains(input.Tag, "-") || len(input.Assets) == 0 || len(input.Assets) > maxAssets {
		return DraftPlan{}, ErrPublish
	}
	result := input
	result.Assets = make([]Asset, len(input.Assets))
	var total int
	previous := ""
	for i, asset := range input.Assets {
		body := bytes.Clone(asset.Body)
		if !assetNameRE.MatchString(asset.Name) || asset.Name <= previous || !mediaRE.MatchString(asset.ContentType) || !digestRE.MatchString(asset.SHA256) || len(body) == 0 || len(body) > maxAssetBytes || total > maxTotalBytes-len(body) || digest(body) != asset.SHA256 {
			return DraftPlan{}, ErrPublish
		}
		previous, total = asset.Name, total+len(body)
		result.Assets[i] = asset
		result.Assets[i].Body = body
	}
	return result, nil
}

func validTag(tag string) bool {
	if !tagRE.MatchString(tag) {
		return false
	}
	if index := strings.IndexByte(tag, '-'); index >= 0 {
		for _, identifier := range strings.Split(tag[index+1:], ".") {
			if len(identifier) > 1 && identifier[0] == '0' && strings.Trim(identifier, "0123456789") == "" {
				return false
			}
		}
	}
	return true
}

func (p *Publisher) expectAbsent(ctx context.Context, evidence *Evidence, requestPath string) error {
	evidence.Phase = "check_absent"
	return p.jsonRequest(ctx, evidence, p.api, http.MethodGet, requestPath, nil, nil, nil, http.StatusNotFound)
}

func (p *Publisher) jsonRequest(ctx context.Context, evidence *Evidence, origin *url.URL, method, requestPath string, query url.Values, input, output any, expectedStatus int) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil || len(encoded) > maxResponseBytes {
			return ErrPublish
		}
		body = bytes.NewReader(encoded)
	}
	request, err := p.request(ctx, evidence, origin, method, requestPath, query, body)
	if err != nil {
		return err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return p.do(request, expectedStatus, output)
}

func (p *Publisher) assetRequest(ctx context.Context, evidence *Evidence, requestPath string, query url.Values, asset Asset, output any) error {
	request, err := p.request(ctx, evidence, p.upload, http.MethodPost, requestPath, query, bytes.NewReader(asset.Body))
	if err != nil {
		return err
	}
	request.ContentLength = int64(len(asset.Body))
	request.Header.Set("Content-Type", asset.ContentType)
	return p.do(request, http.StatusCreated, output)
}

func (p *Publisher) request(ctx context.Context, evidence *Evidence, origin *url.URL, method, requestPath string, query url.Values, body io.Reader) (*http.Request, error) {
	if ctx.Err() != nil || evidence.Requests >= maxRequests || (method != http.MethodGet && method != http.MethodPost) || requestPath == "" || requestPath[0] != '/' || path.Clean(requestPath) != requestPath {
		return nil, ErrPublish
	}
	u := *origin
	u.Path, u.RawQuery = requestPath, query.Encode()
	if u.Scheme != origin.Scheme || u.Host != origin.Host {
		return nil, ErrPublish
	}
	request, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, ErrPublish
	}
	request.Response = nil
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", APIVersion)
	request.Header.Set("User-Agent", "DarwinRouter-create-only-publisher/1")
	evidence.Requests++
	return request, nil
}

func (p *Publisher) do(request *http.Request, expectedStatus int, output any) error {
	response, err := p.client.Do(request)
	if err != nil {
		return ErrPublish
	}
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes+1))
		return ErrPublish
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return ErrPublish
	}
	if output == nil {
		return nil
	}
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "application/json") || json.Unmarshal(body, output) != nil {
		return ErrPublish
	}
	return nil
}

type tagResponse struct {
	Ref    string `json:"ref"`
	Object struct {
		SHA  string `json:"sha"`
		Type string `json:"type"`
	} `json:"object"`
}

type createReleaseRequest struct {
	TagName              string `json:"tag_name"`
	TargetCommitish      string `json:"target_commitish"`
	Name                 string `json:"name"`
	Body                 string `json:"body"`
	Draft                bool   `json:"draft"`
	Prerelease           bool   `json:"prerelease"`
	GenerateReleaseNotes bool   `json:"generate_release_notes"`
}

type releaseResponse struct {
	ID         int64  `json:"id"`
	TagName    string `json:"tag_name"`
	Name       string `json:"name"`
	Body       string `json:"body"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	UploadURL  string `json:"upload_url"`
}

type assetResponse struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
	State       string `json:"state"`
	Digest      string `json:"digest"`
}

func exactTag(response tagResponse, plan DraftPlan) bool {
	return response.Ref == "refs/tags/"+plan.Tag && response.Object.SHA == plan.Commit && response.Object.Type == "commit"
}

func exactRelease(response releaseResponse, plan DraftPlan) bool {
	return response.ID > 0 && response.TagName == plan.Tag && response.Name == plan.Name && response.Body == plan.Body && response.Draft && response.Prerelease == plan.Prerelease
}

func exactAsset(response assetResponse, asset Asset) bool {
	return response.ID > 0 && response.Name == asset.Name && response.Size == int64(len(asset.Body)) && response.ContentType == asset.ContentType && response.State == "uploaded" && response.Digest == asset.SHA256
}

func exactAssets(remote []assetResponse, expected []Asset) bool {
	if len(remote) != len(expected) {
		return false
	}
	sort.Slice(remote, func(i, j int) bool { return remote[i].Name < remote[j].Name })
	identities := make(map[int64]struct{}, len(remote))
	for i := range expected {
		if !exactAsset(remote[i], expected[i]) {
			return false
		}
		if _, duplicate := identities[remote[i].ID]; duplicate {
			return false
		}
		identities[remote[i].ID] = struct{}{}
	}
	return true
}

func evidenceAsset(asset Asset, id int64) EvidenceAsset {
	return EvidenceAsset{ID: id, Name: asset.Name, Size: int64(len(asset.Body)), SHA256: asset.SHA256}
}

func digest(body []byte) string {
	hash := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(hash[:])
}

func (e Evidence) String() string {
	return fmt.Sprintf("%s %s phase=%s requests=%d", e.State, e.Tag, e.Phase, e.Requests)
}
