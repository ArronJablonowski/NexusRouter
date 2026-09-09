package evaluation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type evaluatorFixture struct {
	descriptor EvaluatorDescriptor
	response   EvaluatorResponse
	err        error
	call       func(context.Context, EvaluatorRequest) (EvaluatorResponse, error)
}

func (e *evaluatorFixture) Descriptor() EvaluatorDescriptor { return e.descriptor }
func (e *evaluatorFixture) Evaluate(ctx context.Context, request EvaluatorRequest) (EvaluatorResponse, error) {
	if e.call != nil {
		return e.call(ctx, request)
	}
	return e.response, e.err
}

func validEvaluatorFixture() (*evaluatorFixture, EvaluatorRequest) {
	d := EvaluatorDescriptor{Version: 1, ID: "extension-reviewer", Revision: "implementation-v1", RubricVersion: "rubric-v1"}
	r := EvaluatorRequest{Version: 1, Domain: "code", Requirements: "Review the output", Candidate: "candidate", Evidence: []EvaluatorEvidence{{ID: "tests", Content: "tests passed"}}}
	a := Audit{Version: 1, EvaluatorID: d.ID, RubricVersion: d.RubricVersion, Domain: r.Domain, Verdict: "accept", Confidence: .8, Findings: []AuditFinding{{Summary: "Meets the requirement", EvidenceRefs: []string{"tests"}}}}
	return &evaluatorFixture{descriptor: d, response: EvaluatorResponse{Version: 1, Audit: a}}, r
}

func TestInvokeEvaluatorPinsProvenanceAndOwnsAliases(t *testing.T) {
	evaluator, request := validEvaluatorFixture()
	originalEvidence := append([]EvaluatorEvidence(nil), request.Evidence...)
	evaluator.call = func(_ context.Context, input EvaluatorRequest) (EvaluatorResponse, error) {
		input.Evidence[0].ID = "mutated"
		out := evaluator.response
		return out, nil
	}
	result, err := InvokeEvaluator(context.Background(), evaluator, request, time.Second)
	if err != nil || result.Version != 1 || result.Descriptor != evaluator.descriptor || result.Audit.Verdict != "accept" || request.Evidence[0] != originalEvidence[0] {
		t.Fatal(result, request, err)
	}
	evaluator.response.Audit.Findings[0].Summary = "changed later"
	evaluator.descriptor.ID = "changed-later"
	if result.Descriptor.ID != "extension-reviewer" || result.Audit.Findings[0].Summary != "Meets the requirement" {
		t.Fatal("extension aliases changed returned result", result)
	}
}

func TestInvokeEvaluatorRejectsInvalidAdmissionWithoutCalling(t *testing.T) {
	evaluator, request := validEvaluatorFixture()
	called := false
	evaluator.call = func(context.Context, EvaluatorRequest) (EvaluatorResponse, error) {
		called = true
		return evaluator.response, nil
	}
	var typedNil *evaluatorFixture
	cases := map[string]func() error{
		"nil": func() error { _, err := InvokeEvaluator(context.Background(), nil, request, time.Second); return err },
		"typed-nil": func() error {
			_, err := InvokeEvaluator(context.Background(), typedNil, request, time.Second)
			return err
		},
		"nil-context": func() error { _, err := InvokeEvaluator(nil, evaluator, request, time.Second); return err },
		"timeout":     func() error { _, err := InvokeEvaluator(context.Background(), evaluator, request, 0); return err },
		"version": func() error {
			bad := request
			bad.Version = 2
			_, err := InvokeEvaluator(context.Background(), evaluator, bad, time.Second)
			return err
		},
		"domain": func() error {
			bad := request
			bad.Domain = "bad\ndomain"
			_, err := InvokeEvaluator(context.Background(), evaluator, bad, time.Second)
			return err
		},
		"requirements": func() error {
			bad := request
			bad.Requirements = " "
			_, err := InvokeEvaluator(context.Background(), evaluator, bad, time.Second)
			return err
		},
		"duplicate": func() error {
			bad := request
			bad.Evidence = append(bad.Evidence, EvaluatorEvidence{ID: "tests", Content: "duplicate"})
			_, err := InvokeEvaluator(context.Background(), evaluator, bad, time.Second)
			return err
		},
		"oversized": func() error {
			bad := request
			bad.Candidate = strings.Repeat("x", MaxEvaluatorRequestBytes)
			_, err := InvokeEvaluator(context.Background(), evaluator, bad, time.Second)
			return err
		},
	}
	for name, invoke := range cases {
		t.Run(name, func(t *testing.T) {
			called = false
			if err := invoke(); !errors.Is(err, ErrEvaluator) || called {
				t.Fatal(err, called)
			}
		})
	}
}

func TestInvokeEvaluatorContainsPanicErrorCancellationAndTimeout(t *testing.T) {
	for _, mode := range []string{"invalid-descriptor", "panic-call", "error", "cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			evaluator, request := validEvaluatorFixture()
			ctx := context.Background()
			timeout := time.Second
			switch mode {
			case "invalid-descriptor":
				evaluator.descriptor = EvaluatorDescriptor{}
			case "panic-call":
				evaluator.call = func(context.Context, EvaluatorRequest) (EvaluatorResponse, error) { panic("private") }
			case "error":
				evaluator.err = errors.New("private extension failure")
			case "cancel":
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			case "timeout":
				timeout = time.Millisecond
				evaluator.call = func(ctx context.Context, _ EvaluatorRequest) (EvaluatorResponse, error) {
					<-ctx.Done()
					return EvaluatorResponse{}, ctx.Err()
				}
			}
			_, err := InvokeEvaluator(ctx, evaluator, request, timeout)
			if mode == "cancel" && !errors.Is(err, context.Canceled) || mode == "timeout" && !errors.Is(err, context.DeadlineExceeded) || mode != "cancel" && mode != "timeout" && !errors.Is(err, ErrEvaluator) || strings.Contains(err.Error(), "private") {
				t.Fatal(err)
			}
		})
	}
}

type panicDescriptorEvaluator struct{}

func (*panicDescriptorEvaluator) Descriptor() EvaluatorDescriptor { panic("private descriptor") }
func (*panicDescriptorEvaluator) Evaluate(context.Context, EvaluatorRequest) (EvaluatorResponse, error) {
	panic("must not be called")
}

func TestInvokeEvaluatorContainsDescriptorPanic(t *testing.T) {
	_, request := validEvaluatorFixture()
	if _, err := InvokeEvaluator(context.Background(), &panicDescriptorEvaluator{}, request, time.Second); !errors.Is(err, ErrEvaluator) || strings.Contains(err.Error(), "private") {
		t.Fatal(err)
	}
}

func TestDescribeEvaluatorSupportsSafePreAdmission(t *testing.T) {
	evaluator, _ := validEvaluatorFixture()
	if descriptor, err := DescribeEvaluator(evaluator); err != nil || descriptor != evaluator.descriptor {
		t.Fatal(descriptor, err)
	}
	var typedNil *evaluatorFixture
	if _, err := DescribeEvaluator(typedNil); !errors.Is(err, ErrEvaluator) {
		t.Fatal(err)
	}
	if _, err := DescribeEvaluator(&panicDescriptorEvaluator{}); !errors.Is(err, ErrEvaluator) || strings.Contains(err.Error(), "private") {
		t.Fatal(err)
	}
	evaluator.descriptor.Version = 2
	if _, err := DescribeEvaluator(evaluator); !errors.Is(err, ErrEvaluator) {
		t.Fatal("unknown descriptor version admitted", err)
	}
}

type unstableEvaluator struct{ calls int }

func (e *unstableEvaluator) Descriptor() EvaluatorDescriptor {
	e.calls++
	if e.calls == 1 {
		return EvaluatorDescriptor{Version: 1, ID: "reviewer", Revision: "v1", RubricVersion: "rubric"}
	}
	return EvaluatorDescriptor{Version: 1, ID: "other", Revision: "v1", RubricVersion: "rubric"}
}
func (*unstableEvaluator) Evaluate(context.Context, EvaluatorRequest) (EvaluatorResponse, error) {
	return EvaluatorResponse{Version: 1, Audit: Audit{Version: 1, EvaluatorID: "reviewer", RubricVersion: "rubric", Domain: "code", Verdict: "abstain", Findings: []AuditFinding{}}}, nil
}

func TestInvokeEvaluatorRejectsUnstableAndMalformedResults(t *testing.T) {
	_, request := validEvaluatorFixture()
	if _, err := InvokeEvaluator(context.Background(), &unstableEvaluator{}, request, time.Second); !errors.Is(err, ErrEvaluator) {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*EvaluatorResponse){
		"version":       func(r *EvaluatorResponse) { r.Version = 2 },
		"audit-version": func(r *EvaluatorResponse) { r.Audit.Version = 2 },
		"evaluator":     func(r *EvaluatorResponse) { r.Audit.EvaluatorID = "other" },
		"rubric":        func(r *EvaluatorResponse) { r.Audit.RubricVersion = "other" },
		"domain":        func(r *EvaluatorResponse) { r.Audit.Domain = "creative" },
		"verdict":       func(r *EvaluatorResponse) { r.Audit.Verdict = "maybe" },
		"confidence":    func(r *EvaluatorResponse) { r.Audit.Confidence = 2 },
		"invented-ref":  func(r *EvaluatorResponse) { r.Audit.Findings[0].EvidenceRefs = []string{"invented"} },
		"oversized":     func(r *EvaluatorResponse) { r.Audit.Findings[0].Summary = strings.Repeat("x", MaxAuditBytes) },
	} {
		t.Run(name, func(t *testing.T) {
			evaluator, request := validEvaluatorFixture()
			mutate(&evaluator.response)
			if _, err := InvokeEvaluator(context.Background(), evaluator, request, time.Second); !errors.Is(err, ErrEvaluator) {
				t.Fatal(err)
			}
		})
	}
}
