package app

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

// ObservedToolsProvenanceValidatorID is the stable identity for the stock
// deterministic publication-provenance validator. Its evidence establishes no
// semantic correctness beyond the checks described by Validate.
const ObservedToolsProvenanceValidatorID = "observed-tools-v1"

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
}

var _ skills.Validator = ObservedToolsProvenanceValidator{}

// Validate returns deterministic evidence for the provenance claim only. All
// failures are collapsed to ErrValidation so corrupt private data is not
// exposed through activation errors.
func (v ObservedToolsProvenanceValidator) Validate(ctx context.Context, version skills.Version) (proof skills.Evidence, err error) {
	fail := func() (skills.Evidence, error) { return skills.Evidence{}, skills.ErrValidation }
	defer func() {
		if recover() != nil {
			proof, err = fail()
		}
	}()
	if ctx == nil || version.Validate() != nil || v.Database == "" || nilPublicationStore(v.Publications) {
		return fail()
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if bounded.Err() != nil {
		return fail()
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

	storedBefore, readErr := v.Publications.Load(bounded, candidate.Draft.Key, candidate.ID)
	storedBeforeBody, marshalErr := json.Marshal(storedBefore)
	if readErr != nil || marshalErr != nil || !bytes.Equal(versionBody, storedBeforeBody) {
		return fail()
	}
	before, readErr := v.Publications.PublicationBinding(bounded, candidate.Draft.Key, candidate.ID)
	if readErr != nil || before.Validate() != nil {
		return fail()
	}
	db, openErr := telemetry.OpenReadOnly(bounded, v.Database)
	if openErr != nil {
		return fail()
	}
	snapshot, readErr := db.ObservedToolsValidationSnapshot(bounded, candidate.Draft.Key.Scope, before.AttemptID)
	closeErr := db.Close()
	if readErr != nil || closeErr != nil || bounded.Err() != nil {
		return fail()
	}
	after, readErr := v.Publications.PublicationBinding(bounded, candidate.Draft.Key, candidate.ID)
	if readErr != nil || after != before || after.Validate() != nil || bounded.Err() != nil {
		return fail()
	}
	storedAfter, readErr := v.Publications.Load(bounded, candidate.Draft.Key, candidate.ID)
	storedAfterBody, marshalErr := json.Marshal(storedAfter)
	if readErr != nil || marshalErr != nil || !bytes.Equal(versionBody, storedAfterBody) || bounded.Err() != nil {
		return fail()
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
		return fail()
	}
	for _, required := range candidate.Draft.RequiredTools {
		if !slices.Contains(group.Tools, required) {
			return fail()
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
