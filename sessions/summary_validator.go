package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"
)

var summaryValidatorID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// SummaryValidationInput is an immutable-by-contract view of one durable draft
// and its exact source. Implementations are trusted, deterministic Go code and
// must not mutate the supplied values or perform side effects.
type SummaryValidationInput struct {
	AttemptID, TaskID, SourceDigest, DraftDigest string
	Source                                       Snapshot
	Draft                                        SummaryDraft
}

// SummaryValidationDecision is semantic evidence, not model output. Abstention
// is retained but never authorizes use of the draft.
type SummaryValidationDecision struct {
	Decision string
	Note     string
}

type SummaryValidator interface {
	ValidateSummary(context.Context, SummaryValidationInput) (SummaryValidationDecision, error)
}

type SummaryValidatorFunc func(context.Context, SummaryValidationInput) (SummaryValidationDecision, error)

func (f SummaryValidatorFunc) ValidateSummary(ctx context.Context, in SummaryValidationInput) (SummaryValidationDecision, error) {
	return f(ctx, in)
}

// SummaryValidatorRegistry snapshots trusted host bindings. A changed
// implementation must use a new identity; model-selected identities are never
// resolved implicitly.
type SummaryValidatorRegistry struct{ validators map[string]SummaryValidator }

func NewSummaryValidatorRegistry(input map[string]SummaryValidator) (*SummaryValidatorRegistry, error) {
	if len(input) > 64 {
		return nil, ErrHistory
	}
	owned := make(map[string]SummaryValidator, len(input))
	for id, validator := range input {
		if !summaryValidatorID.MatchString(id) || validator == nil || summaryValidatorNil(validator) {
			return nil, ErrHistory
		}
		// Built-in identities are evidence provenance, not merely labels. Do not
		// let a host bind an arbitrary approving callback under the stock
		// non-authorizing linter's stable identity.
		if id == SummaryIntegrityValidatorID {
			if _, ok := validator.(SummaryIntegrityValidator); !ok {
				return nil, ErrHistory
			}
		}
		owned[id] = validator
	}
	return &SummaryValidatorRegistry{validators: owned}, nil
}

func (r *SummaryValidatorRegistry) Resolve(id string) (SummaryValidator, error) {
	if r == nil || !summaryValidatorID.MatchString(id) {
		return nil, ErrHistory
	}
	validator, ok := r.validators[id]
	if !ok || validator == nil || summaryValidatorNil(validator) {
		return nil, ErrHistory
	}
	return validator, nil
}

func summaryValidatorNil(validator SummaryValidator) bool {
	v := reflect.ValueOf(validator)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func (d SummaryValidationDecision) Validate() error {
	if d.Decision != "approved" && d.Decision != "rejected" && d.Decision != "abstained" {
		return ErrHistory
	}
	if strings.TrimSpace(d.Note) == "" || len(d.Note) > 4096 || !utf8.ValidString(d.Note) {
		return ErrHistory
	}
	return nil
}

// SummaryDraftDigest canonically binds all persisted draft fields, including
// provenance, usage and the proposed compaction request.
func SummaryDraftDigest(d SummaryDraft) (string, error) {
	body, err := json.Marshal(d)
	if err != nil || len(body) == 0 || len(body) > 1<<20 {
		return "", ErrHistory
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}
