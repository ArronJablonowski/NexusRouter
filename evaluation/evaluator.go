package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	EvaluatorContractVersion = 1
	MaxEvaluatorRequestBytes = 1 << 20
	// EvaluatorExtensionProvider is stable host provenance for results produced
	// through this in-process extension contract rather than an LLM provider.
	EvaluatorExtensionProvider = "evaluator_extension"
)

var ErrEvaluator = errors.New("evaluation: evaluator invocation rejected")

// EvaluatorDescriptor is host-validated provenance for an evaluator extension.
// Implementations must return the same descriptor for their lifetime.
type EvaluatorDescriptor struct {
	Version       int    `json:"version"`
	ID            string `json:"id"`
	Revision      string `json:"revision"`
	RubricVersion string `json:"rubric_version"`
}

func (d EvaluatorDescriptor) Validate() error {
	if d.Version != EvaluatorContractVersion || !auditLabel(d.ID) || !auditLabel(d.Revision) || !auditLabel(d.RubricVersion) {
		return ErrEvaluator
	}
	return nil
}

// EvaluatorEvidence is immutable caller-attributed material. Its ID is the
// only reference an extension may cite in its advisory audit.
type EvaluatorEvidence struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

// EvaluatorRequest is a bounded provider-neutral evaluation input. Candidate
// and evidence are untrusted data, never extension configuration.
type EvaluatorRequest struct {
	Version      int                 `json:"version"`
	Domain       string              `json:"domain"`
	Requirements string              `json:"requirements"`
	Candidate    string              `json:"candidate"`
	Evidence     []EvaluatorEvidence `json:"evidence"`
}

// Validate performs the same bounded admission checks used by InvokeEvaluator,
// allowing a host to reject invalid work before it creates a durable attempt.
func (request EvaluatorRequest) Validate() error {
	return validateEvaluatorRequest(request)
}

// EvaluatorResponse is untrusted until InvokeEvaluator binds it to the
// descriptor, request domain, and caller-owned evidence references.
type EvaluatorResponse struct {
	Version int   `json:"version"`
	Audit   Audit `json:"audit"`
}

// EvaluatorResult contains an owned, validated response and host-pinned
// evaluator provenance. Mutating extension-owned inputs or outputs cannot
// change this value after InvokeEvaluator returns.
type EvaluatorResult struct {
	Version    int                 `json:"version"`
	Descriptor EvaluatorDescriptor `json:"descriptor"`
	Audit      Audit               `json:"audit"`
}

// Evaluator is the public provider-neutral extension point. Descriptor must be
// static and side-effect free. Evaluate must honor cancellation and must not
// retain or mutate request data after returning.
type Evaluator interface {
	Descriptor() EvaluatorDescriptor
	Evaluate(context.Context, EvaluatorRequest) (EvaluatorResponse, error)
}

// DescribeEvaluator safely reads and validates static extension provenance so
// a host can bind it into durable admission records before execution.
func DescribeEvaluator(evaluator Evaluator) (descriptor EvaluatorDescriptor, err error) {
	if nilEvaluator(evaluator) {
		return EvaluatorDescriptor{}, ErrEvaluator
	}
	defer func() {
		if recover() != nil {
			descriptor, err = EvaluatorDescriptor{}, ErrEvaluator
		}
	}()
	descriptor = evaluator.Descriptor()
	if descriptor.Validate() != nil {
		return EvaluatorDescriptor{}, ErrEvaluator
	}
	return descriptor, nil
}

// InvokeEvaluator safely invokes one extension. It contains panics, applies a
// cooperative timeout, rejects typed nils and untrusted errors, pins descriptor
// stability, and returns only host-owned validated data.
func InvokeEvaluator(ctx context.Context, evaluator Evaluator, request EvaluatorRequest, timeout time.Duration) (result EvaluatorResult, err error) {
	if ctx == nil || nilEvaluator(evaluator) || timeout <= 0 || timeout > MaxReviewDuration || request.Validate() != nil {
		return EvaluatorResult{}, ErrEvaluator
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer func() {
		if recover() != nil {
			result = EvaluatorResult{}
			if bounded.Err() != nil {
				err = bounded.Err()
			} else {
				err = ErrEvaluator
			}
		}
	}()
	if bounded.Err() != nil {
		return EvaluatorResult{}, bounded.Err()
	}
	descriptor, describeErr := DescribeEvaluator(evaluator)
	if describeErr != nil {
		return EvaluatorResult{}, ErrEvaluator
	}
	owned := ownEvaluatorRequest(request)
	response, callErr := evaluator.Evaluate(bounded, owned)
	if bounded.Err() != nil {
		return EvaluatorResult{}, bounded.Err()
	}
	after, describeErr := DescribeEvaluator(evaluator)
	if callErr != nil || describeErr != nil || after != descriptor {
		return EvaluatorResult{}, ErrEvaluator
	}
	audit, validateErr := validateEvaluatorResponse(response, descriptor, request.Domain, evaluatorEvidenceRefs(request))
	if validateErr != nil {
		return EvaluatorResult{}, ErrEvaluator
	}
	return EvaluatorResult{Version: EvaluatorContractVersion, Descriptor: descriptor, Audit: audit}, nil
}

func validateEvaluatorRequest(request EvaluatorRequest) error {
	if request.Version != EvaluatorContractVersion || !auditLabel(request.Domain) || strings.TrimSpace(request.Requirements) == "" || !utf8.ValidString(request.Requirements) || !utf8.ValidString(request.Candidate) || len(request.Evidence) > 254 {
		return ErrEvaluator
	}
	seen := map[string]bool{"requirements": true, "candidate": true}
	for _, item := range request.Evidence {
		if !auditLabel(item.ID) || seen[item.ID] || !utf8.ValidString(item.Content) {
			return ErrEvaluator
		}
		seen[item.ID] = true
	}
	body, err := json.Marshal(request)
	if err != nil || len(body) > MaxEvaluatorRequestBytes {
		return ErrEvaluator
	}
	return nil
}

func validateEvaluatorResponse(response EvaluatorResponse, descriptor EvaluatorDescriptor, domain string, refs []string) (Audit, error) {
	if response.Version != EvaluatorContractVersion {
		return Audit{}, ErrEvaluator
	}
	body, err := json.Marshal(response.Audit)
	if err != nil || len(body) > MaxAuditBytes {
		return Audit{}, ErrEvaluator
	}
	return ParseAudit(body, AuditContext{EvaluatorID: descriptor.ID, RubricVersion: descriptor.RubricVersion, Domain: domain, AllowedEvidenceRefs: refs})
}

func ownEvaluatorRequest(request EvaluatorRequest) EvaluatorRequest {
	owned := request
	owned.Evidence = append([]EvaluatorEvidence(nil), request.Evidence...)
	return owned
}

func evaluatorEvidenceRefs(request EvaluatorRequest) []string {
	refs := make([]string, 0, len(request.Evidence)+2)
	refs = append(refs, "requirements", "candidate")
	for _, item := range request.Evidence {
		refs = append(refs, item.ID)
	}
	return refs
}

func nilEvaluator(evaluator Evaluator) bool {
	if evaluator == nil {
		return true
	}
	value := reflect.ValueOf(evaluator)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
