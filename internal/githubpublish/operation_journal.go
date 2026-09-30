package githubpublish

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var ErrOperationJournal = errors.New("GitHub publication operation journal failed")

const (
	operationJournalSchema = 1
	operationJournalScope  = "nexusrouter-github-publication-operation"
	maxOperationJournal    = 1 << 20
	maxOperationEvents     = 128

	ReconciliationAbsent         = "absent"
	ReconciliationPartialExact   = "partial_exact"
	ReconciliationExactDraft     = "exact_draft"
	ReconciliationExactPublished = "exact_published"
	ReconciliationConfirmed      = "confirmed"
	ReconciliationConflict       = "conflict"
)

// OperationIdentity is the independently authorized key for one publication
// operation. It contains no credential or release content.
type OperationIdentity struct {
	AuthorizationSHA256 string `json:"authorization_sha256"`
	Repository          string `json:"repository"`
	Tag                 string `json:"tag"`
}

// ExpectedDraftState is the exact public state a create-only publisher may
// establish. Release-note and asset bodies remain outside the journal.
type ExpectedDraftState struct {
	Identity           OperationIdentity `json:"identity"`
	Commit             string            `json:"commit"`
	TagMessage         string            `json:"tag_message"`
	Tagger             Tagger            `json:"tagger"`
	ReleaseTitle       string            `json:"release_title"`
	ReleaseNotesSHA256 string            `json:"release_notes_sha256"`
	Prerelease         bool              `json:"prerelease"`
	MakeLatest         bool              `json:"make_latest"`
	Assets             []ExpectedAsset   `json:"assets"`
}

// ExpectedAsset binds one authorized asset's public name, byte count, and
// content digest without storing its body.
type ExpectedAsset struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type"`
}

// RemoteObservation is supplied by a read-only remote observer. Absence must
// be explicit; an observer error is never converted into an absence claim.
type RemoteObservation struct {
	Tag     *ObservedTag           `json:"tag,omitempty"`
	Release *ObservedRelease       `json:"release,omitempty"`
	Latest  *ObservedLatestRelease `json:"latest,omitempty"`
	Assets  []ObservedAsset        `json:"assets"`
}

type ObservedTag struct {
	Commit     string `json:"commit"`
	ObjectSHA  string `json:"object_sha"`
	ObjectType string `json:"object_type"`
	Message    string `json:"message"`
	Tagger     Tagger `json:"tagger"`
}

type ObservedLatestRelease struct {
	Exists bool   `json:"exists"`
	ID     int64  `json:"id,omitempty"`
	Tag    string `json:"tag,omitempty"`
}

// ObservedRelease is the exact release metadata returned by a read-only
// observer, including the commit resolved independently from its tag.
type ObservedRelease struct {
	ID                 int64  `json:"id"`
	Tag                string `json:"tag"`
	Commit             string `json:"commit"`
	Title              string `json:"title"`
	ReleaseNotesSHA256 string `json:"release_notes_sha256"`
	Draft              bool   `json:"draft"`
	Prerelease         bool   `json:"prerelease"`
	Immutable          bool   `json:"immutable"`
}

// ObservedAsset is independently read-back remote asset metadata.
type ObservedAsset struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type"`
}

// RemoteObserver reads current remote state for exactly one operation key.
type RemoteObserver interface {
	Observe(context.Context, OperationIdentity) (RemoteObservation, error)
}

// ReconciliationResult is a point-in-time read-only classification. Existing
// or previously attempted remote state is never declared retryable.
type ReconciliationResult struct {
	Classification string `json:"classification"`
	RetryAllowed   bool   `json:"retry_allowed"`
	JournalTorn    bool   `json:"journal_torn"`
	Events         int    `json:"events"`
	ReleaseID      int64  `json:"release_id,omitempty"`
	ObservedAssets int    `json:"observed_assets"`
}

type journalHeader struct {
	SchemaVersion  int               `json:"schema_version"`
	Scope          string            `json:"scope"`
	Type           string            `json:"type"`
	Identity       OperationIdentity `json:"identity"`
	ExpectedSHA256 string            `json:"expected_state_sha256"`
}

type journalEvent struct {
	SchemaVersion int               `json:"schema_version"`
	Scope         string            `json:"scope"`
	Type          string            `json:"type"`
	Sequence      int               `json:"sequence"`
	Identity      OperationIdentity `json:"identity"`
	Phase         string            `json:"phase"`
	Asset         string            `json:"asset,omitempty"`
	Outcome       string            `json:"outcome,omitempty"`
	ObjectSHA     string            `json:"object_sha,omitempty"`
}

type journalSnapshot struct {
	header         journalHeader
	events         []journalEvent
	torn           bool
	pending        *journalEvent
	uncertain      bool
	confirmed      bool
	publishAttempt bool
	tagObjectSHA   string
	step           int
}

// OperationJournal is a single-process append capability. Reopening a journal
// is deliberately read-only; recovery must reconcile rather than retry writes.
type OperationJournal struct {
	root     *os.Root
	file     *os.File
	name     string
	identity OperationIdentity
	assets   []string
	step     int
	next     int
	pending  *journalEvent
	terminal bool
	closed   bool
}

// CreateOperationJournal durably creates one exclusive operation record before
// any mutating request. The caller must close the returned capability.
func CreateOperationJournal(path string, expected ExpectedDraftState) (*OperationJournal, error) {
	encodedExpected, err := canonicalExpectedState(expected)
	if err != nil || path == "" {
		return nil, ErrOperationJournal
	}
	parent, name := filepath.Dir(path), filepath.Base(path)
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !assetNameRE.MatchString(name) {
		return nil, ErrOperationJournal
	}
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return nil, ErrOperationJournal
	}
	root, err := os.OpenRoot(realParent)
	if err != nil {
		return nil, ErrOperationJournal
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		root.Close()
		return nil, ErrOperationJournal
	}
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		root.Close()
		return nil, ErrOperationJournal
	}
	header := journalHeader{SchemaVersion: operationJournalSchema, Scope: operationJournalScope,
		Type: "operation", Identity: expected.Identity, ExpectedSHA256: digest(encodedExpected)}
	if err = writeJournalLine(file, header); err != nil || syncRoot(root) != nil {
		file.Close()
		root.Close()
		return nil, ErrOperationJournal
	}
	assets := make([]string, len(expected.Assets))
	for i, asset := range expected.Assets {
		assets[i] = asset.Name
	}
	return &OperationJournal{root: root, file: file, name: name, identity: expected.Identity, assets: assets, next: 1}, nil
}

func (j *OperationJournal) RecordIntent(phase, asset string) error {
	if j == nil || j.closed || j.terminal || j.pending != nil || !matchesPublicationStep(j.step, j.assets, phase, asset) {
		return ErrOperationJournal
	}
	event := journalEvent{SchemaVersion: operationJournalSchema, Scope: operationJournalScope,
		Type: "intent", Sequence: j.next, Identity: j.identity, Phase: phase, Asset: asset}
	if j.append(event) != nil {
		return ErrOperationJournal
	}
	j.next++
	j.pending = &event
	return nil
}

func (j *OperationJournal) RecordResult(phase, asset, outcome string, objectSHA ...string) error {
	sha := ""
	if len(objectSHA) == 1 {
		sha = objectSHA[0]
	} else if len(objectSHA) > 1 {
		return ErrOperationJournal
	}
	if j == nil || j.closed || j.terminal || j.pending == nil ||
		j.pending.Phase != phase || j.pending.Asset != asset || (outcome != "confirmed" && outcome != "uncertain") ||
		(phase == "create_tag_object" && outcome == "confirmed" && !commitRE.MatchString(sha)) ||
		(phase != "create_tag_object" && sha != "") || (outcome == "uncertain" && sha != "") {
		return ErrOperationJournal
	}
	event := journalEvent{SchemaVersion: operationJournalSchema, Scope: operationJournalScope,
		Type: "result", Sequence: j.next, Identity: j.identity, Phase: phase, Asset: asset, Outcome: outcome, ObjectSHA: sha}
	if j.append(event) != nil {
		return ErrOperationJournal
	}
	j.next++
	j.pending = nil
	j.terminal = outcome == "uncertain"
	if outcome == "confirmed" {
		j.step++
	}
	return nil
}

// RecordConfirmation follows a complete read-back of the exact remote draft.
// Reconciliation still re-observes remote state and never trusts this alone.
func (j *OperationJournal) RecordConfirmation() error {
	if j == nil || j.closed || j.terminal || j.pending != nil || j.step != publicationStepCount(len(j.assets)) {
		return ErrOperationJournal
	}
	event := journalEvent{SchemaVersion: operationJournalSchema, Scope: operationJournalScope,
		Type: "confirmation", Sequence: j.next, Identity: j.identity, Phase: "complete", Outcome: "confirmed"}
	if j.append(event) != nil {
		return ErrOperationJournal
	}
	j.next++
	j.terminal = true
	return nil
}

func (j *OperationJournal) append(event journalEvent) error {
	if j.file == nil || j.root == nil {
		return ErrOperationJournal
	}
	pathInfo, err := j.root.Lstat(j.name)
	fileInfo, statErr := j.file.Stat()
	if err != nil || statErr != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(pathInfo, fileInfo) || fileInfo.Size() > maxOperationJournal {
		return ErrOperationJournal
	}
	return writeJournalLine(j.file, event)
}

func (j *OperationJournal) Close() error {
	if j == nil || j.closed {
		return ErrOperationJournal
	}
	j.closed = true
	fileErr := j.file.Close()
	rootErr := j.root.Close()
	if fileErr != nil || rootErr != nil {
		return ErrOperationJournal
	}
	return nil
}

// ReconcileOperation reads the durable journal and one injected remote
// observation. It performs no mutation and has no credential handling.
func ReconcileOperation(ctx context.Context, path string, expected ExpectedDraftState, observer RemoteObserver) (ReconciliationResult, error) {
	var result ReconciliationResult
	encoded, err := canonicalExpectedState(expected)
	if ctx == nil || err != nil || observer == nil || ctx.Err() != nil {
		return result, ErrOperationJournal
	}
	snapshot, err := readOperationJournal(path, expected, digest(encoded))
	if err != nil {
		return result, ErrOperationJournal
	}
	result.JournalTorn, result.Events = snapshot.torn, len(snapshot.events)
	observed, err := observer.Observe(ctx, expected.Identity)
	if err != nil || ctx.Err() != nil {
		return result, ErrOperationJournal
	}
	classification, releaseID, observedAssets := classifyRemote(expected, observed, snapshot.tagObjectSHA)
	result.Classification, result.ReleaseID, result.ObservedAssets = classification, releaseID, observedAssets
	result.RetryAllowed = classification == ReconciliationAbsent && len(snapshot.events) == 0 && !snapshot.torn
	if classification == ReconciliationExactPublished && snapshot.publishAttempt && !snapshot.torn {
		result.Classification = ReconciliationConfirmed
	}
	return result, nil
}

func classifyRemote(expected ExpectedDraftState, observed RemoteObservation, journalTagObjectSHA string) (string, int64, int) {
	if observed.Tag == nil && observed.Release == nil && len(observed.Assets) == 0 {
		return ReconciliationAbsent, 0, 0
	}
	if observed.Tag == nil || observed.Tag.Commit != expected.Commit || observed.Tag.ObjectType != "tag" ||
		!commitRE.MatchString(observed.Tag.ObjectSHA) || journalTagObjectSHA != "" && observed.Tag.ObjectSHA != journalTagObjectSHA ||
		observed.Tag.Message != expected.TagMessage || observed.Tag.Tagger != expected.Tagger {
		return ReconciliationConflict, releaseID(observed.Release), len(observed.Assets)
	}
	if observed.Release == nil {
		if len(observed.Assets) == 0 {
			return ReconciliationPartialExact, 0, 0
		}
		return ReconciliationConflict, 0, len(observed.Assets)
	}
	release := observed.Release
	if release.ID < 1 || release.Tag != expected.Identity.Tag || release.Commit != expected.Commit ||
		release.Title != expected.ReleaseTitle || release.ReleaseNotesSHA256 != expected.ReleaseNotesSHA256 ||
		release.Prerelease != expected.Prerelease || (release.Draft && release.Immutable) || (!release.Draft && !release.Immutable) {
		return ReconciliationConflict, release.ID, len(observed.Assets)
	}
	want := make(map[string]ExpectedAsset, len(expected.Assets))
	for _, asset := range expected.Assets {
		want[asset.Name] = asset
	}
	seenIDs := make(map[int64]bool, len(observed.Assets))
	seenNames := make(map[string]bool, len(observed.Assets))
	for _, asset := range observed.Assets {
		expectedAsset, ok := want[asset.Name]
		if !ok || asset.ID < 1 || seenIDs[asset.ID] || seenNames[asset.Name] || asset.Size != expectedAsset.Size ||
			asset.SHA256 != expectedAsset.SHA256 || asset.ContentType != expectedAsset.ContentType {
			return ReconciliationConflict, release.ID, len(observed.Assets)
		}
		seenIDs[asset.ID], seenNames[asset.Name] = true, true
	}
	if len(observed.Assets) < len(expected.Assets) {
		return ReconciliationPartialExact, release.ID, len(observed.Assets)
	}
	if !release.Draft {
		if !validLatestObservation(observed.Latest) || expected.MakeLatest && (!observed.Latest.Exists || observed.Latest.ID != release.ID || observed.Latest.Tag != expected.Identity.Tag) ||
			!expected.MakeLatest && observed.Latest.Exists && (observed.Latest.ID == release.ID || observed.Latest.Tag == expected.Identity.Tag) {
			return ReconciliationConflict, release.ID, len(observed.Assets)
		}
		return ReconciliationExactPublished, release.ID, len(observed.Assets)
	}
	return ReconciliationExactDraft, release.ID, len(observed.Assets)
}

func validLatestObservation(latest *ObservedLatestRelease) bool {
	if latest == nil {
		return false
	}
	if !latest.Exists {
		return latest.ID == 0 && latest.Tag == ""
	}
	return latest.ID > 0 && validTag(latest.Tag)
}

func releaseID(release *ObservedRelease) int64 {
	if release == nil {
		return 0
	}
	return release.ID
}

func canonicalExpectedState(expected ExpectedDraftState) ([]byte, error) {
	parts := strings.Split(expected.Identity.Repository, "/")
	if len(parts) != 2 || !digestRE.MatchString(expected.Identity.AuthorizationSHA256) ||
		!nameRE.MatchString(parts[0]) || !nameRE.MatchString(parts[1]) || !validTag(expected.Identity.Tag) ||
		!commitRE.MatchString(expected.Commit) || expected.ReleaseTitle != "NexusRouter "+expected.Identity.Tag ||
		!digestRE.MatchString(expected.ReleaseNotesSHA256) || expected.Prerelease != strings.Contains(expected.Identity.Tag, "-") ||
		!validTagger(expected.Tagger) || expected.TagMessage != "NexusRouter release "+expected.Identity.Tag || (expected.Prerelease && expected.MakeLatest) ||
		len(expected.Assets) == 0 || len(expected.Assets) > maxAssets {
		return nil, ErrOperationJournal
	}
	previous := ""
	var total int64
	for _, asset := range expected.Assets {
		if !assetNameRE.MatchString(asset.Name) || asset.Name <= previous || asset.Size < 1 ||
			asset.Size > maxAssetBytes || total > int64(maxTotalBytes)-asset.Size || !digestRE.MatchString(asset.SHA256) || !mediaRE.MatchString(asset.ContentType) {
			return nil, ErrOperationJournal
		}
		total += asset.Size
		previous = asset.Name
	}
	body, err := json.Marshal(expected)
	if err != nil {
		return nil, ErrOperationJournal
	}
	return body, nil
}

func publicationStepCount(assetCount int) int { return 4 + assetCount }

func matchesPublicationStep(step int, assets []string, phase, asset string) bool {
	switch {
	case step == 0:
		return phase == "create_tag_object" && asset == ""
	case step == 1:
		return phase == "create_tag_ref" && asset == ""
	case step == 2:
		return phase == "create_draft" && asset == ""
	case step >= 3 && step < 3+len(assets):
		return phase == "upload_asset" && asset == assets[step-3]
	case step == 3+len(assets):
		return phase == "publish_release" && asset == ""
	default:
		return false
	}
}

func writeJournalLine(file *os.File, value any) error {
	body, err := json.Marshal(value)
	if err != nil || len(body)+1 > maxOperationJournal {
		return ErrOperationJournal
	}
	body = append(body, '\n')
	n, err := file.Write(body)
	if err != nil || n != len(body) || file.Sync() != nil {
		return ErrOperationJournal
	}
	return nil
}

func syncRoot(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return ErrOperationJournal
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil || closeErr != nil {
		return ErrOperationJournal
	}
	return nil
}

func readOperationJournal(path string, expected ExpectedDraftState, expectedSHA string) (journalSnapshot, error) {
	var snapshot journalSnapshot
	identity := expected.Identity
	assets := make([]string, len(expected.Assets))
	for i, asset := range expected.Assets {
		assets[i] = asset.Name
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxOperationJournal {
		return snapshot, ErrOperationJournal
	}
	file, err := os.Open(path)
	if err != nil {
		return snapshot, ErrOperationJournal
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !os.SameFile(info, actual) || actual.Size() != info.Size() {
		return snapshot, ErrOperationJournal
	}
	body, err := io.ReadAll(io.LimitReader(file, maxOperationJournal+1))
	if err != nil || int64(len(body)) != actual.Size() {
		return snapshot, ErrOperationJournal
	}
	if body[len(body)-1] != '\n' {
		snapshot.torn = true
		index := bytes.LastIndexByte(body, '\n')
		if index < 0 {
			return journalSnapshot{}, ErrOperationJournal
		}
		body = body[:index+1]
	}
	lines := bytes.Split(body[:len(body)-1], []byte{'\n'})
	if len(lines) < 1 || len(lines) > maxOperationEvents+1 || decodeCanonicalLine(lines[0], &snapshot.header) != nil ||
		snapshot.header.SchemaVersion != operationJournalSchema || snapshot.header.Scope != operationJournalScope ||
		snapshot.header.Type != "operation" || snapshot.header.Identity != identity || snapshot.header.ExpectedSHA256 != expectedSHA {
		return journalSnapshot{}, ErrOperationJournal
	}
	for i, line := range lines[1:] {
		var event journalEvent
		if decodeCanonicalLine(line, &event) != nil || event.SchemaVersion != operationJournalSchema ||
			event.Scope != operationJournalScope || event.Sequence != i+1 || event.Identity != identity {
			return journalSnapshot{}, ErrOperationJournal
		}
		if applyJournalEvent(&snapshot, event, assets) != nil {
			return journalSnapshot{}, ErrOperationJournal
		}
		snapshot.events = append(snapshot.events, event)
	}
	return snapshot, nil
}

func applyJournalEvent(snapshot *journalSnapshot, event journalEvent, assets []string) error {
	switch event.Type {
	case "intent":
		if snapshot.pending != nil || snapshot.uncertain || snapshot.confirmed || event.Outcome != "" || event.ObjectSHA != "" ||
			!matchesPublicationStep(snapshot.step, assets, event.Phase, event.Asset) {
			return ErrOperationJournal
		}
		copy := event
		snapshot.pending = &copy
		if event.Phase == "publish_release" {
			snapshot.publishAttempt = true
		}
	case "result":
		if snapshot.pending == nil || snapshot.uncertain || snapshot.confirmed || event.Phase != snapshot.pending.Phase ||
			event.Asset != snapshot.pending.Asset || (event.Outcome != "confirmed" && event.Outcome != "uncertain") ||
			(event.Phase == "create_tag_object" && event.Outcome == "confirmed" && !commitRE.MatchString(event.ObjectSHA)) ||
			(event.Phase != "create_tag_object" && event.ObjectSHA != "") || (event.Outcome == "uncertain" && event.ObjectSHA != "") {
			return ErrOperationJournal
		}
		snapshot.pending = nil
		snapshot.uncertain = event.Outcome == "uncertain"
		if event.Outcome == "confirmed" {
			snapshot.step++
			if event.Phase == "create_tag_object" {
				snapshot.tagObjectSHA = event.ObjectSHA
			}
		}
	case "confirmation":
		if snapshot.pending != nil || snapshot.uncertain || snapshot.confirmed || snapshot.step != publicationStepCount(len(assets)) || event.Phase != "complete" || event.Asset != "" || event.Outcome != "confirmed" || event.ObjectSHA != "" {
			return ErrOperationJournal
		}
		snapshot.confirmed = true
	default:
		return ErrOperationJournal
	}
	return nil
}

func decodeCanonicalLine(line []byte, destination any) error {
	if len(line) == 0 || json.Unmarshal(line, destination) != nil {
		return ErrOperationJournal
	}
	canonical, err := json.Marshal(destination)
	if err != nil || !bytes.Equal(line, canonical) {
		return ErrOperationJournal
	}
	return nil
}
