package evaluation

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"unicode/utf8"
)

const MaxAuditBytes = 64 << 10

var ErrAudit = errors.New("invalid or unsupported audit")

// AuditContext is supplied by the orchestrator, never by candidate output.
// Evidence references identify data actually supplied to the evaluator. Pinning
// metadata prevents the evaluator from inventing its identity or task scope.
type AuditContext struct {
	EvaluatorID, RubricVersion, Domain string
	AllowedEvidenceRefs                []string
}

// Audit is an advisory assessment, not proof of execution or a fitness record.
// Even a valid accept verdict requires independent policy/evidence resolution.
// Audit invocation cost must be measured by the caller, not reported by a model.
type Audit struct {
	Version       int            `json:"version"`
	EvaluatorID   string         `json:"evaluator_id"`
	RubricVersion string         `json:"rubric_version"`
	Domain        string         `json:"domain"`
	Verdict       string         `json:"verdict"`
	Confidence    float64        `json:"confidence"`
	Findings      []AuditFinding `json:"findings"`
}

type AuditFinding struct {
	Summary      string   `json:"summary"`
	EvidenceRefs []string `json:"evidence_refs"`
}

// ParseAudit validates a single JSON document. Free text, fenced JSON, nulls,
// duplicate/unknown fields and references absent from trusted context fail shut.
// Abstention is represented distinctly and never converted to accepted evidence.
func ParseAudit(body []byte, trusted AuditContext) (Audit, error) {
	bad := func() (Audit, error) { return Audit{}, ErrAudit }
	if len(body) > MaxAuditBytes || !utf8.Valid(body) || !auditLabel(trusted.EvaluatorID) || !auditLabel(trusted.RubricVersion) || !auditLabel(trusted.Domain) || len(trusted.AllowedEvidenceRefs) > 256 {
		return bad()
	}
	allowed := map[string]bool{}
	for _, ref := range trusted.AllowedEvidenceRefs {
		if !auditLabel(ref) || allowed[ref] {
			return bad()
		}
		allowed[ref] = true
	}
	fields, err := auditObject(body, "version", "evaluator_id", "rubric_version", "domain", "verdict", "confidence", "findings")
	if err != nil {
		return bad()
	}
	var out Audit
	if json.Unmarshal(fields["version"], &out.Version) != nil || out.Version != 1 || auditString(fields["evaluator_id"], &out.EvaluatorID) != nil || out.EvaluatorID != trusted.EvaluatorID || auditString(fields["rubric_version"], &out.RubricVersion) != nil || out.RubricVersion != trusted.RubricVersion || auditString(fields["domain"], &out.Domain) != nil || out.Domain != trusted.Domain || auditString(fields["verdict"], &out.Verdict) != nil {
		return bad()
	}
	if out.Verdict != "accept" && out.Verdict != "reject" && out.Verdict != "abstain" {
		return bad()
	}
	if json.Unmarshal(fields["confidence"], &out.Confidence) != nil || math.IsNaN(out.Confidence) || math.IsInf(out.Confidence, 0) || out.Confidence < 0 || out.Confidence > 1 {
		return bad()
	}
	var findings []json.RawMessage
	if json.Unmarshal(fields["findings"], &findings) != nil || len(findings) > 64 || (len(findings) == 0 && out.Verdict != "abstain") {
		return bad()
	}
	out.Findings = make([]AuditFinding, 0, len(findings))
	for _, raw := range findings {
		fields, err := auditObject(raw, "summary", "evidence_refs")
		var finding AuditFinding
		if err != nil || auditString(fields["summary"], &finding.Summary) != nil || strings.TrimSpace(finding.Summary) == "" || len(finding.Summary) > 4096 {
			return bad()
		}
		var refs []json.RawMessage
		if json.Unmarshal(fields["evidence_refs"], &refs) != nil || len(refs) == 0 || len(refs) > 64 {
			return bad()
		}
		seen := map[string]bool{}
		for _, rawRef := range refs {
			var ref string
			if auditString(rawRef, &ref) != nil || !allowed[ref] || seen[ref] {
				return bad()
			}
			seen[ref] = true
			finding.EvidenceRefs = append(finding.EvidenceRefs, ref)
		}
		out.Findings = append(out.Findings, finding)
	}
	return out, nil
}

func auditLabel(s string) bool {
	return len(s) > 0 && len(s) <= 128 && strings.TrimSpace(s) == s && strings.IndexFunc(s, func(r rune) bool { return r < 32 || r == 127 }) < 0
}

func auditString(raw json.RawMessage, target *string) error {
	if len(raw) == 0 || raw[0] != '"' {
		return ErrAudit
	}
	return json.Unmarshal(raw, target)
}

// Every listed field is required. Reject aliases and duplicates rather than
// relying on struct decoding, which silently accepts both.
func auditObject(body []byte, names ...string) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(body))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return nil, ErrAudit
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || fields[key] != nil {
			return nil, ErrAudit
		}
		known := false
		for _, name := range names {
			known = known || name == key
		}
		if !known {
			return nil, ErrAudit
		}
		var raw json.RawMessage
		if d.Decode(&raw) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, ErrAudit
		}
		fields[key] = raw
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') || len(fields) != len(names) {
		return nil, ErrAudit
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, ErrAudit
	}
	return fields, nil
}
