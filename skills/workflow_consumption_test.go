package skills

import (
	"encoding/json"
	"strings"
	"testing"
)

func consumptionFixture() WorkflowScanConsumption {
	b, _ := NewWorkflowBucket(procedureFixture("a", "a", "read"))
	return WorkflowScanConsumption{Version: 1, Scope: "project", Name: "learning", Domain: "code", Epoch: 1, Revision: 1, PageDigest: strings.Repeat("a", 64), Considered: 1, Eligible: 1, Buckets: []WorkflowBucket{b}}
}

func TestWorkflowConsumptionValidationAndJSON(t *testing.T) {
	c := consumptionFixture()
	body, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"page_digest"`, `"sources"`, `"algorithm"`, `"considered"`, `"eligible"`} {
		if !strings.Contains(string(body), key) {
			t.Fatal("JSON contract", key)
		}
	}
	var decoded WorkflowScanConsumption
	if json.Unmarshal(body, &decoded) != nil || decoded.Validate() != nil {
		t.Fatal("roundtrip")
	}
	c.Considered = 0
	c.Eligible = 0
	c.Buckets = []WorkflowBucket{}
	if c.Validate() != nil {
		t.Fatal("empty receipt")
	}
	for _, mode := range []string{"version", "scope", "name", "domain", "epoch", "revision", "digest", "negative", "many", "eligible", "eligible-without-bucket", "nil", "bucket-domain", "order"} {
		c := consumptionFixture()
		switch mode {
		case "version":
			c.Version = 2
		case "scope":
			c.Scope = ""
		case "name":
			c.Name = "../bad"
		case "domain":
			c.Domain = "other"
		case "epoch":
			c.Epoch = 0
		case "revision":
			c.Revision = 1_000_000_001
		case "digest":
			c.PageDigest = strings.Repeat("A", 64)
		case "negative":
			c.Considered = -1
		case "many":
			c.Considered = 21
		case "eligible":
			c.Eligible = 0
		case "eligible-without-bucket":
			c.Buckets = []WorkflowBucket{}
		case "nil":
			c.Buckets = nil
		case "bucket-domain":
			c.Buckets[0].Sources[0].Domain = "other"
		case "order":
			c.Buckets = append(c.Buckets, c.Buckets[0])
			c.Considered = 2
			c.Eligible = 2
		}
		if c.Validate() == nil {
			t.Fatal("accepted", mode)
		}
	}
}

func TestWorkflowScanPageTransition(t *testing.T) {
	p := scanPageFixture()
	previous := p.Scan
	previous.Revision = 1
	previous.Cursor = p.After
	if p.ValidateAfter(&previous) != nil {
		t.Fatal("continuation")
	}
	first := p
	first.Scan.Revision = 1
	first.After = ""
	if first.ValidateAfter(nil) != nil {
		t.Fatal("first")
	}
	if p.ValidateAfter(nil) == nil {
		t.Fatal("noninitial first")
	}
	for _, mode := range []string{"scope", "name", "domain", "revision", "epoch", "fence", "upper", "cursor", "invalid"} {
		prev := previous
		switch mode {
		case "scope":
			prev.Scope = "other"
		case "name":
			prev.Name = "other"
		case "domain":
			prev.Domain = "other"
		case "revision":
			prev.Revision = 2
		case "epoch":
			prev.Epoch = 2
			prev.Revision = 2
		case "fence":
			prev.Fence = 2
		case "upper":
			prev.Upper = "task-zz"
		case "cursor":
			prev.Cursor = "task-aa"
		case "invalid":
			prev.Version = 2
		}
		if p.ValidateAfter(&prev) == nil {
			t.Fatal("accepted", mode)
		}
	}
	previous.Complete = true
	next := p
	next.After = ""
	next.Scan.Epoch = 2
	if next.ValidateAfter(&previous) != nil {
		t.Fatal("new epoch")
	}
	for _, mode := range []string{"epoch", "after", "fence", "upper"} {
		q := next
		prev := previous
		switch mode {
		case "epoch":
			q.Scan.Epoch = 1
		case "after":
			q.After = "task-a"
		case "fence":
			prev.Fence = 2
		case "upper":
			prev.Upper = "task-zz"
		}
		if q.ValidateAfter(&prev) == nil {
			t.Fatal("new epoch accepted", mode)
		}
	}
}
