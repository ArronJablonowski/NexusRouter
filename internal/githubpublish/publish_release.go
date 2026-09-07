package githubpublish

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Tagger is the exact public identity and whole-second timestamp embedded in
// the authorized annotated tag object. It contains no signing credential.
type Tagger struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Date  string `json:"date"`
}

// PublicationPlan is the complete, externally authorized mutation plan. The
// journal path must name a new file in an existing trusted directory.
type PublicationPlan struct {
	DraftPlan
	TagMessage  string
	Tagger      Tagger
	MakeLatest  bool
	JournalPath string
}

type PublicationEvidence struct {
	SchemaVersion       int             `json:"schema_version"`
	Scope               string          `json:"scope"`
	Repository          string          `json:"repository"`
	AuthorizationSHA256 string          `json:"authorization_sha256"`
	Tag                 string          `json:"tag"`
	Commit              string          `json:"commit"`
	TagObjectSHA        string          `json:"tag_object_sha,omitempty"`
	ReleaseID           int64           `json:"release_id,omitempty"`
	State               string          `json:"state"`
	Phase               string          `json:"phase"`
	RetryAllowed        bool            `json:"retry_allowed"`
	Requests            int             `json:"requests"`
	Published           bool            `json:"published"`
	Immutable           bool            `json:"immutable"`
	UploadedAssets      []EvidenceAsset `json:"uploaded_assets"`
}

// PublishRelease performs the one authorized create-only publication state
// machine. Every mutation is preceded and followed by a durable journal event.
func (p *Publisher) PublishRelease(ctx context.Context, input PublicationPlan) (PublicationEvidence, error) {
	evidence := PublicationEvidence{SchemaVersion: 1, Scope: "darwinrouter-github-authorized-publication", State: "not_started", Phase: "validate", RetryAllowed: true, UploadedAssets: []EvidenceAsset{}}
	plan, err := validatePublicationPlan(input)
	if err != nil || ctx == nil || ctx.Err() != nil || p == nil {
		return evidence, ErrPublish
	}
	evidence.Repository, evidence.AuthorizationSHA256 = plan.Owner+"/"+plan.Repository, plan.AuthorizationSHA256
	evidence.Tag, evidence.Commit = plan.Tag, plan.Commit
	requestEvidence := Evidence{}
	base := "/repos/" + plan.Owner + "/" + plan.Repository

	evidence.Phase = "verify_immutable_policy"
	var policy immutableReleasePolicy
	if err = p.jsonRequest(ctx, &requestEvidence, p.api, "GET", base+"/immutable-releases", nil, nil, &policy, 200); err != nil || !policy.Enabled {
		copyRequests(&evidence, requestEvidence)
		return evidence, ErrPublish
	}
	if err = p.expectAbsent(ctx, &requestEvidence, base+"/git/ref/tags/"+url.PathEscape(plan.Tag)); err != nil {
		copyRequests(&evidence, requestEvidence)
		return evidence, err
	}
	if err = p.expectAbsent(ctx, &requestEvidence, base+"/releases/tags/"+url.PathEscape(plan.Tag)); err != nil {
		copyRequests(&evidence, requestEvidence)
		return evidence, err
	}

	expected := journalExpected(plan)
	journal, err := CreateOperationJournal(plan.JournalPath, expected)
	if err != nil {
		copyRequests(&evidence, requestEvidence)
		return evidence, ErrPublish
	}
	closed := false
	defer func() {
		if !closed {
			_ = journal.Close()
		}
	}()
	evidence.State, evidence.RetryAllowed = "uncertain", false

	evidence.Phase = "create_tag_object"
	var tag annotatedTagResponse
	if journal.RecordIntent(evidence.Phase, "") != nil {
		copyRequests(&evidence, requestEvidence)
		return evidence, ErrPublish
	}
	err = func() error {
		request := annotatedTagRequest{Tag: plan.Tag, Message: plan.TagMessage, Object: plan.Commit, Type: "commit", Tagger: plan.Tagger}
		if requestErr := p.jsonRequest(ctx, &requestEvidence, p.api, "POST", base+"/git/tags", nil, request, &tag, 201); requestErr != nil || !exactAnnotatedTag(tag, request) {
			return ErrPublish
		}
		return nil
	}()
	if err != nil {
		_ = journal.RecordResult(evidence.Phase, "", "uncertain")
		copyRequests(&evidence, requestEvidence)
		return evidence, ErrPublish
	}
	if journal.RecordResult(evidence.Phase, "", "confirmed", tag.SHA) != nil {
		copyRequests(&evidence, requestEvidence)
		return evidence, ErrPublish
	}
	evidence.TagObjectSHA = tag.SHA

	evidence.Phase = "create_tag_ref"
	err = journalMutation(journal, evidence.Phase, "", func() error {
		var ref tagResponse
		input := struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		}{Ref: "refs/tags/" + plan.Tag, SHA: tag.SHA}
		if requestErr := p.jsonRequest(ctx, &requestEvidence, p.api, "POST", base+"/git/refs", nil, input, &ref, 201); requestErr != nil || !exactAnnotatedRef(ref, plan, tag.SHA) {
			return ErrPublish
		}
		return nil
	})
	if err != nil {
		copyRequests(&evidence, requestEvidence)
		return evidence, ErrPublish
	}

	evidence.Phase = "create_draft"
	var release releaseResponse
	err = journalMutation(journal, evidence.Phase, "", func() error {
		request := createReleaseRequest{TagName: plan.Tag, TargetCommitish: plan.Commit, Name: plan.Name, Body: plan.Body, Draft: true, Prerelease: plan.Prerelease, GenerateReleaseNotes: false}
		if requestErr := p.jsonRequest(ctx, &requestEvidence, p.api, "POST", base+"/releases", nil, request, &release, 201); requestErr != nil || !exactPublicationRelease(release, plan, true, false) {
			return ErrPublish
		}
		return nil
	})
	if err != nil {
		copyRequests(&evidence, requestEvidence)
		return evidence, ErrPublish
	}
	evidence.ReleaseID = release.ID
	expectedUpload := p.upload.String() + base + "/releases/" + strconv.FormatInt(release.ID, 10) + "/assets{?name,label}"
	if release.UploadURL != expectedUpload {
		copyRequests(&evidence, requestEvidence)
		return evidence, ErrPublish
	}

	for _, asset := range plan.Assets {
		evidence.Phase = "upload_asset"
		err = journalMutation(journal, evidence.Phase, asset.Name, func() error {
			var remote assetResponse
			query := url.Values{"name": {asset.Name}}
			if requestErr := p.assetRequest(ctx, &requestEvidence, base+"/releases/"+strconv.FormatInt(release.ID, 10)+"/assets", query, asset, &remote); requestErr != nil || !exactAsset(remote, asset) {
				return ErrPublish
			}
			evidence.UploadedAssets = append(evidence.UploadedAssets, evidenceAsset(asset, remote.ID))
			return nil
		})
		if err != nil {
			copyRequests(&evidence, requestEvidence)
			return evidence, ErrPublish
		}
	}

	if err = p.verifyPublicationState(ctx, &requestEvidence, base, plan, release.ID, tag.SHA, true, false); err != nil {
		copyRequests(&evidence, requestEvidence)
		return evidence, ErrPublish
	}
	evidence.Phase = "recheck_immutable_policy"
	policy = immutableReleasePolicy{}
	if err = p.jsonRequest(ctx, &requestEvidence, p.api, "GET", base+"/immutable-releases", nil, nil, &policy, 200); err != nil || !policy.Enabled {
		copyRequests(&evidence, requestEvidence)
		return evidence, ErrPublish
	}
	evidence.Phase = "publish_release"
	err = journalMutation(journal, evidence.Phase, "", func() error {
		request := updateReleaseRequest{TagName: plan.Tag, TargetCommitish: plan.Commit, Name: plan.Name, Body: plan.Body, Draft: false, Prerelease: plan.Prerelease, MakeLatest: strconv.FormatBool(plan.MakeLatest)}
		var published releaseResponse
		if requestErr := p.jsonRequest(ctx, &requestEvidence, p.api, "PATCH", base+"/releases/"+strconv.FormatInt(release.ID, 10), nil, request, &published, 200); requestErr != nil || !exactPublicationRelease(published, plan, false, true) || published.ID != release.ID {
			return ErrPublish
		}
		return nil
	})
	if err != nil {
		copyRequests(&evidence, requestEvidence)
		return evidence, ErrPublish
	}
	evidence.Published = true
	if err = p.verifyPublicationState(ctx, &requestEvidence, base, plan, release.ID, tag.SHA, false, true); err != nil {
		copyRequests(&evidence, requestEvidence)
		return evidence, ErrPublish
	}
	if err = journal.RecordConfirmation(); err != nil || journal.Close() != nil {
		copyRequests(&evidence, requestEvidence)
		return evidence, ErrPublish
	}
	closed = true
	copyRequests(&evidence, requestEvidence)
	evidence.State, evidence.Phase, evidence.Immutable = "confirmed_published", "complete", true
	return evidence, nil
}

func validatePublicationPlan(input PublicationPlan) (PublicationPlan, error) {
	draft, err := validateAndClone(input.DraftPlan)
	if err != nil || input.JournalPath == "" || input.TagMessage != "DarwinRouter release "+input.Tag ||
		!validTagger(input.Tagger) || (input.Prerelease && input.MakeLatest) {
		return PublicationPlan{}, ErrPublish
	}
	input.DraftPlan = draft
	return input, nil
}

func validTagger(tagger Tagger) bool {
	when, err := time.Parse("2006-01-02T15:04:05Z", tagger.Date)
	return err == nil && when.Format("2006-01-02T15:04:05Z") == tagger.Date && len(tagger.Name) > 0 && len(tagger.Name) <= 200 &&
		len(tagger.Email) >= 3 && len(tagger.Email) <= 254 && strings.Contains(tagger.Email, "@") &&
		!strings.ContainsAny(tagger.Name+tagger.Email, "\r\n\x00")
}

func journalExpected(plan PublicationPlan) ExpectedDraftState {
	assets := make([]ExpectedAsset, len(plan.Assets))
	for i, asset := range plan.Assets {
		assets[i] = ExpectedAsset{Name: asset.Name, Size: int64(len(asset.Body)), SHA256: asset.SHA256, ContentType: asset.ContentType}
	}
	return ExpectedDraftState{
		Identity: OperationIdentity{AuthorizationSHA256: plan.AuthorizationSHA256, Repository: plan.Owner + "/" + plan.Repository, Tag: plan.Tag},
		Commit:   plan.Commit, TagMessage: plan.TagMessage, Tagger: plan.Tagger, ReleaseTitle: plan.Name,
		ReleaseNotesSHA256: plan.ReleaseNotesSHA256, Prerelease: plan.Prerelease, MakeLatest: plan.MakeLatest, Assets: assets,
	}
}

func journalMutation(journal *OperationJournal, phase, asset string, mutate func() error) error {
	if journal.RecordIntent(phase, asset) != nil {
		return ErrPublish
	}
	err := mutate()
	outcome := "confirmed"
	if err != nil {
		outcome = "uncertain"
	}
	if journal.RecordResult(phase, asset, outcome) != nil || err != nil {
		return ErrPublish
	}
	return nil
}

func copyRequests(evidence *PublicationEvidence, requests Evidence) {
	evidence.Requests = requests.Requests
}

type immutableReleasePolicy struct {
	Enabled         bool `json:"enabled"`
	EnforcedByOwner bool `json:"enforced_by_owner"`
}

type annotatedTagRequest struct {
	Tag     string `json:"tag"`
	Message string `json:"message"`
	Object  string `json:"object"`
	Type    string `json:"type"`
	Tagger  Tagger `json:"tagger"`
}

type annotatedTagResponse struct {
	SHA     string `json:"sha"`
	Tag     string `json:"tag"`
	Message string `json:"message"`
	Object  struct {
		SHA  string `json:"sha"`
		Type string `json:"type"`
	} `json:"object"`
	Tagger Tagger `json:"tagger"`
}

type updateReleaseRequest struct {
	TagName         string `json:"tag_name"`
	TargetCommitish string `json:"target_commitish"`
	Name            string `json:"name"`
	Body            string `json:"body"`
	Draft           bool   `json:"draft"`
	Prerelease      bool   `json:"prerelease"`
	MakeLatest      string `json:"make_latest"`
}

func exactAnnotatedTag(response annotatedTagResponse, request annotatedTagRequest) bool {
	return commitRE.MatchString(response.SHA) && response.Tag == request.Tag && response.Message == request.Message &&
		response.Object.SHA == request.Object && response.Object.Type == request.Type && response.Tagger == request.Tagger
}

func exactAnnotatedRef(response tagResponse, plan PublicationPlan, tagObjectSHA string) bool {
	return response.Ref == "refs/tags/"+plan.Tag && response.Object.SHA == tagObjectSHA && response.Object.Type == "tag"
}

func exactPublicationRelease(response releaseResponse, plan PublicationPlan, draft, immutable bool) bool {
	return response.ID > 0 && response.TagName == plan.Tag && response.Name == plan.Name &&
		response.Body == plan.Body && response.Draft == draft && response.Prerelease == plan.Prerelease && response.Immutable == immutable
}

func (p *Publisher) verifyPublicationState(ctx context.Context, evidence *Evidence, base string, plan PublicationPlan, releaseID int64, tagObjectSHA string, draft, immutable bool) error {
	var release releaseResponse
	path := base + "/releases/" + strconv.FormatInt(releaseID, 10)
	if err := p.jsonRequest(ctx, evidence, p.api, "GET", path, nil, nil, &release, 200); err != nil ||
		!exactPublicationRelease(release, plan, draft, immutable) || release.ID != releaseID {
		return ErrPublish
	}
	var assets []assetResponse
	if err := p.jsonRequest(ctx, evidence, p.api, "GET", path+"/assets", url.Values{"page": {"1"}, "per_page": {"100"}}, nil, &assets, 200); err != nil || !exactAssets(assets, plan.Assets) {
		return ErrPublish
	}
	var ref tagResponse
	if err := p.jsonRequest(ctx, evidence, p.api, "GET", base+"/git/ref/tags/"+url.PathEscape(plan.Tag), nil, nil, &ref, 200); err != nil || !exactAnnotatedRef(ref, plan, tagObjectSHA) {
		return ErrPublish
	}
	var tag annotatedTagResponse
	request := annotatedTagRequest{Tag: plan.Tag, Message: plan.TagMessage, Object: plan.Commit, Type: "commit", Tagger: plan.Tagger}
	if err := p.jsonRequest(ctx, evidence, p.api, "GET", base+"/git/tags/"+tagObjectSHA, nil, nil, &tag, 200); err != nil || !exactAnnotatedTag(tag, request) || tag.SHA != tagObjectSHA {
		return ErrPublish
	}
	if !draft {
		latest, err := p.observeLatestRelease(ctx, evidence, base)
		if err != nil || plan.MakeLatest && (!latest.Exists || latest.ID != releaseID || latest.Tag != plan.Tag) ||
			!plan.MakeLatest && latest.Exists && (latest.ID == releaseID || latest.Tag == plan.Tag) {
			return ErrPublish
		}
	}
	return nil
}

func (p *Publisher) observeLatestRelease(ctx context.Context, evidence *Evidence, base string) (ObservedLatestRelease, error) {
	var result ObservedLatestRelease
	request, err := p.request(ctx, evidence, p.api, http.MethodGet, base+"/releases/latest", nil, nil)
	if err != nil {
		return result, ErrPublish
	}
	response, err := p.client.Do(request)
	if err != nil {
		return result, ErrPublish
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		n, copyErr := io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes+1))
		if copyErr != nil || n > maxResponseBytes {
			return result, ErrPublish
		}
		return result, nil
	}
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "application/json") {
		return result, ErrPublish
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	var release releaseResponse
	if err != nil || len(body) > maxResponseBytes || json.Unmarshal(body, &release) != nil || release.ID < 1 || !validTag(release.TagName) {
		return result, ErrPublish
	}
	return ObservedLatestRelease{Exists: true, ID: release.ID, Tag: release.TagName}, nil
}
