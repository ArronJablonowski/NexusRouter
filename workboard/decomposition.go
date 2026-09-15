package workboard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

const (
	DecompositionPolicyVersion       = 1
	DefaultDecompositionMaxDepth     = 4
	DefaultDecompositionMaxChildren  = 8
	DefaultDecompositionConfigDigest = "9ed6989409857cebc63a70606febd6bcbe50e43fea09983d3c87a2b629ba88bf"
)

// DecompositionLimits bound the parent/child work hierarchy. Dependency DAG
// limits are intentionally separate: a dependency is not delegated child work.
type DecompositionLimits struct {
	Version     int `json:"version"`
	MaxDepth    int `json:"max_depth"`
	MaxChildren int `json:"max_children"`
}

func DefaultDecompositionLimits() DecompositionLimits {
	return DecompositionLimits{Version: DecompositionPolicyVersion, MaxDepth: DefaultDecompositionMaxDepth, MaxChildren: DefaultDecompositionMaxChildren}
}

func (l DecompositionLimits) Validate() error {
	if l.Version != DecompositionPolicyVersion || l.MaxDepth < 1 || l.MaxDepth > MaxGraphDepth ||
		l.MaxChildren < 1 || l.MaxChildren > MaxChildrenPerParent {
		return fail(CodeInvalid, "decomposition_limits")
	}
	return nil
}

// DecompositionPolicy is trusted host policy. ConfigDigest binds the complete
// effective host configuration; PolicyDigest binds this exact policy shape.
type DecompositionPolicy struct {
	Version      int                 `json:"version"`
	Limits       DecompositionLimits `json:"limits"`
	ConfigDigest string              `json:"config_digest"`
	PolicyDigest string              `json:"policy_digest"`
}

func NewDecompositionPolicy(limits DecompositionLimits, configDigest string) (DecompositionPolicy, error) {
	p := DecompositionPolicy{Version: DecompositionPolicyVersion, Limits: limits, ConfigDigest: configDigest}
	if limits.Validate() != nil || !digest(configDigest) {
		return DecompositionPolicy{}, fail(CodeInvalid, "decomposition_policy")
	}
	var err error
	p.PolicyDigest, err = decompositionPolicyDigest(p)
	if err != nil {
		return DecompositionPolicy{}, err
	}
	return p, nil
}

func (p DecompositionPolicy) Validate() error {
	want, err := decompositionPolicyDigest(p)
	if err != nil || p.Version != DecompositionPolicyVersion || p.Limits.Validate() != nil || !digest(p.ConfigDigest) ||
		!digest(p.PolicyDigest) || p.PolicyDigest != want {
		return fail(CodeInvalid, "decomposition_policy")
	}
	return nil
}

// Restrict derives child policy without permitting either dimension to grow.
// The effective configuration identity remains inherited from the parent.
func (p DecompositionPolicy) Restrict(limits DecompositionLimits) (DecompositionPolicy, error) {
	if p.Validate() != nil || limits.Validate() != nil || limits.MaxDepth > p.Limits.MaxDepth || limits.MaxChildren > p.Limits.MaxChildren {
		return DecompositionPolicy{}, fail(CodeInvalid, "decomposition_restriction")
	}
	return NewDecompositionPolicy(limits, p.ConfigDigest)
}

// Inherit returns a policy no broader than either the current host policy or
// the durable parent admission. A requested policy may narrow it again.
func (p DecompositionPolicy) Inherit(parent DecompositionAdmission, requested *DecompositionLimits) (DecompositionPolicy, error) {
	if p.Validate() != nil || parent.Validate() != nil {
		return DecompositionPolicy{}, fail(CodeInvalid, "decomposition_inheritance")
	}
	limits := p.Limits
	if parent.Limits.MaxDepth < limits.MaxDepth {
		limits.MaxDepth = parent.Limits.MaxDepth
	}
	if parent.Limits.MaxChildren < limits.MaxChildren {
		limits.MaxChildren = parent.Limits.MaxChildren
	}
	inherited, err := NewDecompositionPolicy(limits, p.ConfigDigest)
	if err != nil || requested == nil {
		return inherited, err
	}
	return inherited.Restrict(*requested)
}

func decompositionPolicyDigest(p DecompositionPolicy) (string, error) {
	copy := p
	copy.PolicyDigest = ""
	body, err := json.Marshal(copy)
	if err != nil {
		return "", fail(CodeInvalid, "decomposition_policy")
	}
	sum := sha256.Sum256(append([]byte("darwin.workboard.decomposition.policy.v1\x00"), body...))
	return hex.EncodeToString(sum[:]), nil
}

// DecompositionDecision is the deterministic, pre-allocation result of
// evaluating one hierarchy placement. Durable admission identity, actor and
// runtime provenance are added transactionally by the store.
type DecompositionDecision struct {
	Version        int                 `json:"version"`
	BoardID        string              `json:"board_id"`
	ParentID       string              `json:"parent_id,omitempty"`
	Limits         DecompositionLimits `json:"limits"`
	ConfigDigest   string              `json:"config_digest"`
	PolicyDigest   string              `json:"policy_digest"`
	Depth          int                 `json:"depth"`
	DirectChildren int                 `json:"direct_children"`
	Digest         string              `json:"digest"`
}

func (a DecompositionDecision) Validate() error {
	want, err := decompositionDecisionDigest(a)
	if err != nil || a.Version != DecompositionPolicyVersion || !validID(a.BoardID) || !optionalID(a.ParentID) ||
		a.Limits.Validate() != nil || !digest(a.ConfigDigest) || !digest(a.PolicyDigest) || !digest(a.Digest) || a.Digest != want ||
		a.Depth < 1 || a.Depth > a.Limits.MaxDepth || a.DirectChildren < 0 || a.DirectChildren > a.Limits.MaxChildren ||
		(a.ParentID == "" && (a.Depth != 1 || a.DirectChildren != 0)) {
		return fail(CodeInvalid, "decomposition_admission")
	}
	policy := DecompositionPolicy{Version: DecompositionPolicyVersion, Limits: a.Limits, ConfigDigest: a.ConfigDigest, PolicyDigest: a.PolicyDigest}
	if policy.Validate() != nil {
		return fail(CodeInvalid, "decomposition_admission")
	}
	return nil
}

func decompositionDecisionDigest(a DecompositionDecision) (string, error) {
	copy := a
	copy.Digest = ""
	body, err := json.Marshal(copy)
	if err != nil {
		return "", fail(CodeInvalid, "decomposition_admission")
	}
	sum := sha256.Sum256(append([]byte("darwin.workboard.decomposition.decision.v1\x00"), body...))
	return hex.EncodeToString(sum[:]), nil
}

// AdmitDecomposition evaluates only the parent forest. cardID is empty for a
// not-yet-allocated create and identifies the moved card for reparenting.
func AdmitDecomposition(graph Graph, expectedGraphRevision int64, cardID, parentID string, policy DecompositionPolicy) (DecompositionDecision, error) {
	if policy.Validate() != nil || !optionalID(cardID) || !optionalID(parentID) || cardID != "" && cardID == parentID {
		return DecompositionDecision{}, fail(CodeInvalid, "decomposition")
	}
	if _, err := ValidateGraph(graph, expectedGraphRevision); err != nil {
		return DecompositionDecision{}, err
	}
	byID := make(map[string]Node, len(graph.Nodes))
	for _, node := range graph.Nodes {
		byID[node.ID] = node
	}
	if cardID != "" {
		if _, ok := byID[cardID]; !ok {
			return DecompositionDecision{}, fail(CodeMissingNode, "card")
		}
	}
	depth, children := 1, 0
	if parentID != "" {
		if _, ok := byID[parentID]; !ok {
			return DecompositionDecision{}, fail(CodeMissingNode, "parent")
		}
		for _, node := range graph.Nodes {
			if node.ParentID == parentID && node.ID != cardID {
				children++
			}
		}
		children++
		seen := map[string]bool{}
		for current := parentID; current != ""; current = byID[current].ParentID {
			if current == cardID || seen[current] {
				return DecompositionDecision{}, fail(CodeCycle, "parent")
			}
			seen[current] = true
			depth++
		}
	}
	deepest := depth
	if cardID != "" {
		childrenByParent := make(map[string][]string, len(graph.Nodes))
		for _, node := range graph.Nodes {
			if node.ParentID != "" && node.ID != cardID {
				childrenByParent[node.ParentID] = append(childrenByParent[node.ParentID], node.ID)
			}
		}
		var subtreeHeight func(string) int
		subtreeHeight = func(id string) int {
			height := 1
			for _, child := range childrenByParent[id] {
				if candidate := 1 + subtreeHeight(child); candidate > height {
					height = candidate
				}
			}
			return height
		}
		deepest += subtreeHeight(cardID) - 1
	}
	if deepest > policy.Limits.MaxDepth {
		return DecompositionDecision{}, fail(CodeDepthExhausted, "parent_depth")
	}
	if children > policy.Limits.MaxChildren {
		return DecompositionDecision{}, fail(CodeLimitExceeded, "parent_children")
	}
	a := DecompositionDecision{Version: DecompositionPolicyVersion, BoardID: graph.BoardID, ParentID: parentID, Limits: policy.Limits,
		ConfigDigest: policy.ConfigDigest, PolicyDigest: policy.PolicyDigest, Depth: depth, DirectChildren: children}
	var err error
	a.Digest, err = decompositionDecisionDigest(a)
	if err != nil || a.Validate() != nil {
		return DecompositionDecision{}, fail(CodeInvalid, "decomposition_decision")
	}
	return a, nil
}

// DecompositionRuntimeOrigin is trusted execution provenance attached by the
// host. Its zero value represents an operator/API mutation outside an agent
// tool call; partial origins are invalid.
type DecompositionRuntimeOrigin struct {
	TaskID     string `json:"task_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	TurnID     string `json:"turn_id,omitempty"`
	AttemptID  string `json:"attempt_id,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	ModelID    string `json:"model_id,omitempty"`
	ProviderID string `json:"provider_id,omitempty"`
}

func (o DecompositionRuntimeOrigin) empty() bool { return o == (DecompositionRuntimeOrigin{}) }

func (o DecompositionRuntimeOrigin) validate() error {
	if o.empty() {
		return nil
	}
	for _, field := range []struct {
		value string
		max   int
	}{{o.TaskID, 128}, {o.SessionID, 128}, {o.TurnID, 128}, {o.AttemptID, 128}, {o.ToolCallID, 256}, {o.ToolName, 64}, {o.ModelID, MaxExecutionModelBytes}, {o.ProviderID, 128}} {
		if !boundedText(field.value, field.max, false) {
			return fail(CodeInvalid, "decomposition_origin")
		}
	}
	return nil
}

// DecompositionAdmission is the immutable durable authority for one applied
// hierarchy mutation. The store allocates identity/time and binds the exact
// operation, request, decision, actor, inherited parent admission and origin.
type DecompositionAdmission struct {
	Version               int                        `json:"version"`
	AdmissionID           string                     `json:"admission_id"`
	OperationID           string                     `json:"operation_id"`
	RequestDigest         string                     `json:"request_digest"`
	DecisionDigest        string                     `json:"decision_digest"`
	BoardID               string                     `json:"board_id"`
	CardID                string                     `json:"card_id"`
	ParentID              string                     `json:"parent_id,omitempty"`
	Actor                 Actor                      `json:"actor"`
	Origin                DecompositionRuntimeOrigin `json:"origin"`
	ParentAdmissionID     string                     `json:"parent_admission_id,omitempty"`
	ParentAdmissionDigest string                     `json:"parent_admission_digest,omitempty"`
	Limits                DecompositionLimits        `json:"limits"`
	ConfigDigest          string                     `json:"config_digest"`
	PolicyDigest          string                     `json:"policy_digest"`
	Depth                 int                        `json:"depth"`
	DirectChildren        int                        `json:"direct_children"`
	AdmittedAt            time.Time                  `json:"admitted_at"`
	AdmissionDigest       string                     `json:"admission_digest"`
}

func (a DecompositionAdmission) Validate() error {
	want, err := a.CanonicalDigest()
	if err != nil || a.Version != DecompositionPolicyVersion || !validID(a.AdmissionID) || !validKey(a.OperationID) ||
		!digest(a.RequestDigest) || !digest(a.DecisionDigest) || !validID(a.BoardID) || !validID(a.CardID) || !optionalID(a.ParentID) ||
		a.Actor.Validate() != nil || a.Origin.validate() != nil || (a.ParentAdmissionID == "") != (a.ParentAdmissionDigest == "") ||
		!optionalID(a.ParentAdmissionID) || a.ParentAdmissionDigest != "" && !digest(a.ParentAdmissionDigest) ||
		a.Limits.Validate() != nil || !digest(a.ConfigDigest) || !digest(a.PolicyDigest) ||
		a.Depth < 1 || a.Depth > a.Limits.MaxDepth || a.DirectChildren < 0 || a.DirectChildren > a.Limits.MaxChildren ||
		(a.ParentID == "" && (a.Depth != 1 || a.DirectChildren != 0 || a.ParentAdmissionID != "")) ||
		!validTime(a.AdmittedAt) || !digest(a.AdmissionDigest) || a.AdmissionDigest != want {
		return fail(CodeInvalid, "decomposition_admission")
	}
	policy := DecompositionPolicy{Version: DecompositionPolicyVersion, Limits: a.Limits, ConfigDigest: a.ConfigDigest, PolicyDigest: a.PolicyDigest}
	if policy.Validate() != nil {
		return fail(CodeInvalid, "decomposition_admission")
	}
	decision := DecompositionDecision{Version: DecompositionPolicyVersion, BoardID: a.BoardID, ParentID: a.ParentID, Limits: a.Limits,
		ConfigDigest: a.ConfigDigest, PolicyDigest: a.PolicyDigest, Depth: a.Depth, DirectChildren: a.DirectChildren, Digest: a.DecisionDigest}
	if decision.Validate() != nil {
		return fail(CodeInvalid, "decomposition_admission")
	}
	return nil
}

func (a DecompositionAdmission) CanonicalDigest() (string, error) {
	copy := a
	copy.AdmissionDigest = ""
	body, err := json.Marshal(copy)
	if err != nil {
		return "", fail(CodeInvalid, "decomposition_admission")
	}
	sum := sha256.Sum256(append([]byte("darwin.workboard.decomposition.admission.v1\x00"), body...))
	return hex.EncodeToString(sum[:]), nil
}
