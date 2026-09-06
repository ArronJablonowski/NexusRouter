package workers

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func attentionFixture() LeaseAttention {
	now := time.Unix(100, 0).UTC()
	return LeaseAttention{Version: 1, ID: "attention-a", TaskID: "task", Writer: true, State: "open", Reason: "expired_unreleased", FirstObserved: now, UpdatedAt: now, LeaseExpires: now}
}

func TestLeaseAttentionValidation(t *testing.T) {
	for _, state := range []string{"open", "lease_released", "lease_renewed"} {
		a := attentionFixture()
		if state != "open" {
			a.State = "resolved"
			a.Reason = state
		}
		if state == "lease_renewed" {
			a.LeaseExpires = a.UpdatedAt.Add(time.Second)
		}
		if a.Validate() != nil {
			t.Fatal(a)
		}
	}
	for name, mutate := range map[string]func(*LeaseAttention){
		"version": func(a *LeaseAttention) { a.Version = 2 }, "id": func(a *LeaseAttention) { a.ID = "bad\n" }, "task": func(a *LeaseAttention) { a.TaskID = "" },
		"state": func(a *LeaseAttention) { a.State = "unknown" }, "reason": func(a *LeaseAttention) { a.Reason = "unknown" },
		"future-open":     func(a *LeaseAttention) { a.LeaseExpires = a.UpdatedAt.Add(time.Second) },
		"expired-renewal": func(a *LeaseAttention) { a.State = "resolved"; a.Reason = "lease_renewed" },
		"state-reason":    func(a *LeaseAttention) { a.State = "resolved" }, "observations": func(a *LeaseAttention) { a.FirstObserved = a.UpdatedAt.Add(time.Second) },
		"first-zero": func(a *LeaseAttention) { a.FirstObserved = time.Time{} }, "updated-range": func(a *LeaseAttention) { a.UpdatedAt = time.Date(2261, 1, 1, 0, 0, 0, 0, time.UTC) },
		"expires-zone": func(a *LeaseAttention) { a.LeaseExpires = a.LeaseExpires.In(time.FixedZone("offset", 3600)) },
	} {
		t.Run(name, func(t *testing.T) {
			a := attentionFixture()
			mutate(&a)
			if a.Validate() == nil {
				t.Fatal("invalid record accepted")
			}
		})
	}
}

func TestLeaseAttentionOptionsValidation(t *testing.T) {
	for _, state := range []string{"open", "resolved", "all"} {
		for _, limit := range []int{1, 100} {
			if (LeaseAttentionOptions{State: state, After: "after", Limit: limit}).Validate() != nil {
				t.Fatal(state, limit)
			}
		}
	}
	for _, o := range []LeaseAttentionOptions{{Limit: 0, State: "open"}, {Limit: 101, State: "open"}, {Limit: 1}, {Limit: 1, State: "bad"}, {Limit: 1, State: "open", After: "a\nb"}, {Limit: 1, State: "open", After: strings.Repeat("x", 129)}} {
		if o.Validate() == nil {
			t.Fatal(o)
		}
	}
}

func TestLeaseAttentionPageValidation(t *testing.T) {
	valid := func() LeaseAttentionPage {
		return LeaseAttentionPage{Version: 1, StorageSchema: 24, Available: true, Items: []LeaseAttention{attentionFixture()}}
	}
	for schema := 1; schema <= 29; schema++ {
		p := valid()
		p.StorageSchema = schema
		p.Available = schema >= 24
		p.Items = []LeaseAttention{}
		if p.Validate() != nil {
			t.Fatal(schema)
		}
	}
	p := valid()
	p.HasMore = true
	p.NextCursor = p.Items[0].ID
	if p.Validate() != nil {
		t.Fatal("cursor rejected")
	}
	for name, mutate := range map[string]func(*LeaseAttentionPage){
		"version": func(p *LeaseAttentionPage) { p.Version = 2 }, "future": func(p *LeaseAttentionPage) { p.StorageSchema = 30 }, "availability": func(p *LeaseAttentionPage) { p.Available = false },
		"nil": func(p *LeaseAttentionPage) { p.Items = nil }, "legacy-items": func(p *LeaseAttentionPage) { p.StorageSchema = 23; p.Available = false },
		"missing-cursor": func(p *LeaseAttentionPage) { p.HasMore = true }, "extra-cursor": func(p *LeaseAttentionPage) { p.NextCursor = p.Items[0].ID },
		"wrong-cursor": func(p *LeaseAttentionPage) { p.HasMore = true; p.NextCursor = "different" }, "empty-more": func(p *LeaseAttentionPage) { p.Items = []LeaseAttention{}; p.HasMore = true; p.NextCursor = "a" },
		"duplicate": func(p *LeaseAttentionPage) { p.Items = append(p.Items, p.Items[0]) }, "unsorted": func(p *LeaseAttentionPage) { a := p.Items[0]; a.ID = "a"; p.Items = append(p.Items, a) },
		"invalid-item": func(p *LeaseAttentionPage) { p.Items[0].TaskID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			p := valid()
			mutate(&p)
			if p.Validate() == nil {
				t.Fatal("invalid page accepted")
			}
		})
	}
	p = valid()
	p.Items = []LeaseAttention{}
	for i := 0; i < 100; i++ {
		a := attentionFixture()
		a.ID = fmt.Sprintf("attention-%03d", i)
		p.Items = append(p.Items, a)
	}
	if p.Validate() != nil {
		t.Fatal("100 items rejected")
	}
	a := attentionFixture()
	a.ID = "last"
	p.Items = append(p.Items, a)
	if p.Validate() == nil {
		t.Fatal("101 items accepted")
	}
}
