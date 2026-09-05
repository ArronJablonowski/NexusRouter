package approvals

import (
	"strings"
	"testing"
)

func TestListOptionsBounds(t *testing.T) {
	for _, q := range []ListOptions{{TaskID: "task", Limit: 1}, {TaskID: strings.Repeat("a", 128), AfterCallID: strings.Repeat("b", 128), Limit: 100}} {
		if q.Validate() != nil {
			t.Fatal(q)
		}
	}
	for _, q := range []ListOptions{{TaskID: "", Limit: 1}, {TaskID: "task", Limit: 0}, {TaskID: "task", Limit: 101}, {TaskID: "task", Limit: -1}, {TaskID: "task/other", Limit: 1}, {TaskID: "task", AfterCallID: " ", Limit: 1}, {TaskID: strings.Repeat("a", 129), Limit: 1}, {TaskID: "task", AfterCallID: strings.Repeat("b", 129), Limit: 1}} {
		if q.Validate() != ErrInvalid {
			t.Fatal(q)
		}
	}
}

func TestListPageValidation(t *testing.T) {
	r := Record{Request: validRequest(), State: Pending}
	valid := func() Page {
		return Page{Version: Version, Query: ListOptions{TaskID: r.Request.TaskID, Limit: 1}, Records: []Record{r}, NextAfterCallID: r.Request.ToolCallID}
	}
	if valid().Validate() != nil {
		t.Fatal("valid page rejected")
	}
	for _, mutate := range []func(*Page){
		func(p *Page) { p.Version = 2 }, func(p *Page) { p.Query.Limit = 0 }, func(p *Page) { p.Query.TaskID = "other" }, func(p *Page) { p.Query.AfterCallID = r.Request.ToolCallID }, func(p *Page) { p.NextAfterCallID = "other" }, func(p *Page) { p.Query.Limit = 2 }, func(p *Page) { p.Records = nil }, func(p *Page) { p.Records[0].State = "unknown" }, func(p *Page) { p.Records = append(p.Records, r) },
	} {
		p := valid()
		mutate(&p)
		if p.Validate() != ErrInvalid {
			t.Fatalf("invalid page admitted: %+v", p)
		}
	}
	p := valid()
	p.NextAfterCallID = ""
	if p.Validate() != nil {
		t.Fatal("terminal full page rejected")
	}
	p.Records = []Record{}
	if p.Validate() != nil {
		t.Fatal("empty page rejected")
	}
	p.Records = nil
	if p.Validate() != ErrInvalid {
		t.Fatal("missing records accepted as empty page")
	}
	p = valid()
	p.Query.Limit = 2
	p.NextAfterCallID = ""
	p.Records = append(p.Records, r)
	if p.Validate() != ErrInvalid {
		t.Fatal("duplicate call admitted")
	}
	p.Records[1].Request.ToolCallID = "call-2"
	if p.Validate() != ErrInvalid {
		t.Fatal("duplicate approval ID admitted across ordered calls")
	}
	p.Records[1].Request.ID = "approval-2"
	if p.Validate() != nil {
		t.Fatal("distinct ordered approvals rejected")
	}
}
