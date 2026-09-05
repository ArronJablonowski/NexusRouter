package skills

import (
	"encoding/json"
	"strings"
	"testing"
)

func scanFixture() WorkflowScan {
	return WorkflowScan{Version: 1, Scope: "project", Name: "learning", Domain: "code", Epoch: 1, Revision: 2, Fence: 1, Cursor: "task-b", Upper: "task-z"}
}

func scanPageFixture() WorkflowScanPage {
	return WorkflowScanPage{Version: 1, Scan: scanFixture(), After: "task-a", Limit: 1, Page: WorkflowCandidatePage{Version: 1, Domain: "code", Candidates: []WorkflowCandidate{procedureFixture("task-b", "session-b").Candidate}, Scanned: 1, Next: "task-b"}}
}

func TestWorkflowScanValidBoundariesAndJSON(t *testing.T) {
	for _, s := range []WorkflowScan{scanFixture(), {Version: 1, Scope: "p", Name: "n", Domain: "d", Epoch: 1, Revision: 1, Complete: true}, {Version: 1, Scope: "p", Name: "n", Domain: "d", Epoch: 1_000_000_000, Revision: 1_000_000_000, Fence: 1, Cursor: "z", Upper: "z", Complete: true}, {Version: 1, Scope: "p", Name: "n", Domain: "d", Epoch: 1, Revision: 1, Fence: 1, Upper: strings.Repeat("a", 128)}} {
		if s.Validate() != nil {
			t.Fatal("valid scan rejected", s)
		}
	}
	encoded, err := json.Marshal(scanPageFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"version"`, `"scope"`, `"epoch"`, `"revision"`, `"fence"`, `"cursor"`, `"upper"`, `"complete"`, `"after"`, `"limit"`, `"page"`} {
		if !strings.Contains(string(encoded), key) {
			t.Fatal("missing JSON contract", key)
		}
	}
	var decoded WorkflowScanPage
	if json.Unmarshal(encoded, &decoded) != nil || decoded.Validate() != nil {
		t.Fatal("JSON roundtrip invalid")
	}
}

func TestWorkflowScanRejectsMalformedState(t *testing.T) {
	for _, mode := range []string{"negative-fence", "missing-fence", "empty-fenced", "version", "scope", "name", "domain", "zero-epoch", "negative-epoch", "revision-before-epoch", "large-revision", "cursor-invalid", "upper-invalid", "cursor-after-upper", "empty-upper-open", "cursor-equal-upper-open"} {
		s := scanFixture()
		switch mode {
		case "negative-fence":
			s.Fence = -1
		case "missing-fence":
			s.Fence = 0
		case "empty-fenced":
			s.Upper = ""
			s.Cursor = ""
			s.Complete = true
		case "version":
			s.Version = 2
		case "scope":
			s.Scope = ""
		case "name":
			s.Name = "../bad"
		case "domain":
			s.Domain = strings.Repeat("a", 65)
		case "zero-epoch":
			s.Epoch = 0
		case "negative-epoch":
			s.Epoch = -1
		case "revision-before-epoch":
			s.Epoch = 3
		case "large-revision":
			s.Revision = 1_000_000_001
		case "cursor-invalid":
			s.Cursor = strings.Repeat("a", 129)
		case "upper-invalid":
			s.Upper = "bad\x00"
		case "cursor-after-upper":
			s.Cursor = "zz"
		case "empty-upper-open":
			s.Cursor = ""
			s.Upper = ""
		case "cursor-equal-upper-open":
			s.Cursor = s.Upper
		}
		if s.Validate() == nil {
			t.Fatal("malformed scan accepted", mode)
		}
	}
}

func TestWorkflowScanPageCompletionAndEmptyObservation(t *testing.T) {
	p := scanPageFixture()
	if p.Validate() != nil {
		t.Fatal(p)
	}
	p.Scan.Upper = p.Scan.Cursor
	p.Scan.Complete = true
	if p.Validate() != nil {
		t.Fatal("full terminal page rejected")
	}
	p = scanPageFixture()
	p.Limit = 2
	p.Page.Next = ""
	p.Scan.Complete = true
	if p.Validate() != nil {
		t.Fatal("short terminal page rejected")
	}
	p = scanPageFixture()
	p.Page.Scanned = 0
	p.Page.Candidates = []WorkflowCandidate{}
	p.Page.Next = ""
	p.Scan.Cursor = p.After
	p.Scan.Complete = true
	if p.Validate() != nil {
		t.Fatal("empty terminal observation rejected")
	}
	p.After = ""
	p.Scan.Cursor = ""
	p.Scan.Upper = ""
	p.Scan.Fence = 0
	if p.Validate() != nil {
		t.Fatal("empty epoch rejected")
	}
}

func TestWorkflowScanPageRejectsInconsistentBindings(t *testing.T) {
	for _, mode := range []string{"version", "scan", "limit-zero", "limit-high", "after-invalid", "after-beyond-cursor", "domain", "zero-scan-advanced", "positive-scan-stationary", "next-mismatch", "candidate-after-cursor", "candidate-after-upper", "complete-full", "incomplete-short", "incomplete-upper", "null-candidates"} {
		p := scanPageFixture()
		switch mode {
		case "version":
			p.Version = 2
		case "scan":
			p.Scan.Revision = 0
		case "limit-zero":
			p.Limit = 0
		case "limit-high":
			p.Limit = 21
		case "after-invalid":
			p.After = strings.Repeat("a", 129)
		case "after-beyond-cursor":
			p.After = "task-c"
		case "domain":
			p.Page.Domain = "creative"
		case "zero-scan-advanced":
			p.Page.Scanned = 0
			p.Page.Candidates = []WorkflowCandidate{}
			p.Page.Next = ""
			p.Scan.Complete = true
		case "positive-scan-stationary":
			p.Scan.Cursor = p.After
		case "next-mismatch":
			p.Page.Next = "task-c"
		case "candidate-after-cursor":
			p.Limit = 2
			p.Page.Next = ""
			p.Page.Candidates[0].TaskID = "task-c"
			p.Scan.Complete = true
		case "candidate-after-upper":
			p.Page.Candidates[0].TaskID = "task-zz"
		case "complete-full":
			p.Scan.Complete = true
		case "incomplete-short":
			p.Limit = 2
			p.Page.Next = ""
		case "incomplete-upper":
			p.Scan.Cursor = p.Scan.Upper
			p.Page.Next = p.Scan.Upper
		case "null-candidates":
			p.Page.Candidates = nil
		}
		if p.Validate() == nil {
			t.Fatal("inconsistent page accepted", mode)
		}
	}
}
