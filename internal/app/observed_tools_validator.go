package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

// ObservedToolsProvenanceValidatorID is the stable identity for the stock
// deterministic publication-provenance validator. Its evidence establishes no
// semantic correctness beyond the checks described by Validate.
const ObservedToolsProvenanceValidatorID = "darwin_observed_tools_activation_v1"

// ObservedToolsProvenanceValidator proves that an immutable generated skill is
// still bound to fresh, successful observed-tool records. It is deliberately
// read-only and never interprets or executes candidate-authored validation
// cases. The stock daemon/SDK registration is a separate integration concern.
type ObservedToolsProvenanceValidator struct {
	Publications skills.PublicationStore
	Database     string
	// LocalOnly is the current host policy. Turning it on invalidates a public
	// candidate even when every historical source allowed cloud disclosure.
	LocalOnly bool
	root      string
	scope     string
}

var _ skills.Validator = ObservedToolsProvenanceValidator{}

// Validate returns deterministic evidence for the provenance claim only.
// Readable source-evidence drift returns a deterministic failed proof so the
// same policy can drive rollback. Operational, structural, cancellation and
// concurrency failures collapse to ErrValidation and never authorize rollback.
func (v ObservedToolsProvenanceValidator) Validate(ctx context.Context, version skills.Version) (proof skills.Evidence, err error) {
	fail := func() (skills.Evidence, error) { return skills.Evidence{}, skills.ErrValidation }
	reject := func() (skills.Evidence, error) {
		return skills.Evidence{ID: ObservedToolsProvenanceValidatorID, Deterministic: true}, nil
	}
	defer func() {
		if recover() != nil {
			proof, err = fail()
		}
	}()
	if ctx == nil || version.Validate() != nil || v.Database == "" {
		return fail()
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if bounded.Err() != nil {
		return fail()
	}
	publications := v.Publications
	if nilPublicationStore(publications) {
		if v.root == "" || v.scope == "" {
			return fail()
		}
		opened, openErr := skills.OpenReadOnly(v.root, []string{v.scope})
		if openErr != nil {
			return fail()
		}
		defer func() {
			if opened.Close() != nil {
				proof, err = fail()
			}
		}()
		publications = opened
	}

	// Snapshot the callback input before invoking either store. ValidationCases
	// remain inert bytes throughout this validator.
	versionBody, marshalErr := json.Marshal(version)
	if marshalErr != nil {
		return fail()
	}
	var candidate skills.Version
	if json.Unmarshal(versionBody, &candidate) != nil || candidate.Validate() != nil {
		return fail()
	}

	storedBefore, readErr := publications.Load(bounded, candidate.Draft.Key, candidate.ID)
	storedBeforeBody, marshalErr := json.Marshal(storedBefore)
	if readErr != nil || marshalErr != nil || !bytes.Equal(versionBody, storedBeforeBody) {
		return fail()
	}
	before, readErr := publications.PublicationBinding(bounded, candidate.Draft.Key, candidate.ID)
	if readErr != nil || before.Validate() != nil {
		return fail()
	}
	db, openErr := telemetry.OpenReadOnly(bounded, v.Database)
	if openErr != nil {
		return fail()
	}
	snapshot, readErr := db.ObservedToolsValidationSnapshot(bounded, candidate.Draft.Key.Scope, before.AttemptID)
	closeErr := db.Close()
	evidenceRejected := errors.Is(readErr, telemetry.ErrObservedToolsEvidence)
	if (readErr != nil && !evidenceRejected) || closeErr != nil || bounded.Err() != nil {
		return fail()
	}
	after, readErr := publications.PublicationBinding(bounded, candidate.Draft.Key, candidate.ID)
	if readErr != nil || after != before || after.Validate() != nil || bounded.Err() != nil {
		return fail()
	}
	storedAfter, readErr := publications.Load(bounded, candidate.Draft.Key, candidate.ID)
	storedAfterBody, marshalErr := json.Marshal(storedAfter)
	if readErr != nil || marshalErr != nil || !bytes.Equal(versionBody, storedAfterBody) || bounded.Err() != nil {
		return fail()
	}
	if evidenceRejected {
		return reject()
	}

	attempt, selection, group := snapshot.Attempt, snapshot.Selection, snapshot.Group
	digest, digestErr := skills.GenerationAttemptDigest(attempt)
	if digestErr != nil || digest != before.AttemptDigest || attempt.ID != before.AttemptID || attempt.Key != candidate.Draft.Key || attempt.Result == nil || selection.ID != attempt.ID || selection.Key != attempt.Key || selection.Algorithm != skills.ObservedToolsAlgorithm || group.ID != selection.Group || group.Algorithm != selection.Algorithm {
		return fail()
	}
	draftBody, marshalErr := json.Marshal(attempt.Result.Draft)
	candidateDraftBody, candidateMarshalErr := json.Marshal(candidate.Draft)
	if marshalErr != nil || candidateMarshalErr != nil || !bytes.Equal(draftBody, candidateDraftBody) {
		return fail()
	}

	wantSessions := make([]string, 0, len(selection.Sources))
	wantEvidence := make([]string, 0, len(selection.Sources))
	localOnly := false
	for _, source := range selection.Sources {
		wantSessions = append(wantSessions, source.SessionID)
		wantEvidence = append(wantEvidence, source.EvaluationDigest)
		localOnly = localOnly || source.Privacy != "cloud_allowed"
	}
	slices.Sort(wantSessions)
	wantSessions = slices.Compact(wantSessions)
	slices.Sort(wantEvidence)
	wantEvidence = slices.Compact(wantEvidence)
	if !slices.Equal(attempt.SourceSessions, wantSessions) || !slices.Equal(attempt.SourceEvidence, wantEvidence) || snapshot.LocalOnly != localOnly || ((v.LocalOnly || localOnly) && candidate.Draft.Privacy != skills.PrivacyLocalOnly) {
		return reject()
	}
	for _, required := range candidate.Draft.RequiredTools {
		if !slices.Contains(group.Tools, required) {
			return reject()
		}
	}
	return skills.Evidence{ID: ObservedToolsProvenanceValidatorID, Passed: true, Deterministic: true}, nil
}

func nilPublicationStore(store skills.PublicationStore) bool {
	if store == nil {
		return true
	}
	value := reflect.ValueOf(store)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	}
	return false
}
