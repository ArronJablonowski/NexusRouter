package workers

import (
	"math"
	"testing"
	"time"
)

func historyFixture() LeaseAttentionHistoryPage {
	a := attentionFixture()
	b := a
	b.State = "resolved"
	b.Reason = "lease_released"
	b.UpdatedAt = b.UpdatedAt.Add(time.Second)
	return LeaseAttentionHistoryPage{Version: 1, StorageSchema: 25, Available: true, AttentionID: a.ID, Items: []LeaseAttentionTransition{{Version: 1, Sequence: 1, Kind: "baseline", Observation: a}, {Version: 1, Sequence: 2, Kind: "observed", Observation: b}}}
}

func TestLeaseAttentionHistoryValidation(t *testing.T) {
	if historyFixture().Validate() != nil {
		t.Fatal("valid history rejected")
	}
	for name, mutate := range map[string]func(*LeaseAttentionHistoryPage){
		"version": func(p *LeaseAttentionHistoryPage) { p.Version = 2 }, "future": func(p *LeaseAttentionHistoryPage) { p.StorageSchema = 30 }, "available": func(p *LeaseAttentionHistoryPage) { p.Available = false },
		"id": func(p *LeaseAttentionHistoryPage) { p.AttentionID = "wrong" }, "nil": func(p *LeaseAttentionHistoryPage) { p.Items = nil }, "legacy": func(p *LeaseAttentionHistoryPage) { p.StorageSchema = 24; p.Available = false },
		"gap": func(p *LeaseAttentionHistoryPage) { p.Items[1].Sequence = 3 }, "duplicate": func(p *LeaseAttentionHistoryPage) { p.Items[1].Sequence = 1 }, "baseline": func(p *LeaseAttentionHistoryPage) { p.Items[1].Kind = "baseline" },
		"kind": func(p *LeaseAttentionHistoryPage) { p.Items[0].Kind = "invented" }, "record-version": func(p *LeaseAttentionHistoryPage) { p.Items[0].Version = 2 }, "sequence": func(p *LeaseAttentionHistoryPage) { p.Items[0].Sequence = 0 },
		"task": func(p *LeaseAttentionHistoryPage) { p.Items[1].Observation.TaskID = "other" }, "writer": func(p *LeaseAttentionHistoryPage) { p.Items[1].Observation.Writer = false }, "first": func(p *LeaseAttentionHistoryPage) {
			p.Items[1].Observation.FirstObserved = p.Items[1].Observation.FirstObserved.Add(time.Nanosecond)
		},
		"time": func(p *LeaseAttentionHistoryPage) {
			p.Items[1].Observation.UpdatedAt = p.Items[0].Observation.UpdatedAt.Add(-time.Second)
		}, "missing-cursor": func(p *LeaseAttentionHistoryPage) { p.HasMore = true }, "extra-cursor": func(p *LeaseAttentionHistoryPage) { p.NextSequence = 2 }, "wrong-cursor": func(p *LeaseAttentionHistoryPage) { p.HasMore = true; p.NextSequence = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			p := historyFixture()
			mutate(&p)
			if p.Validate() == nil {
				t.Fatal("invalid accepted")
			}
		})
	}
	for schema := 1; schema <= 29; schema++ {
		p := historyFixture()
		p.StorageSchema = schema
		p.Available = schema >= 25
		p.Items = []LeaseAttentionTransition{}
		if p.Validate() != nil {
			t.Fatal(schema)
		}
	}
	p := historyFixture()
	p.HasMore = true
	p.NextSequence = 2
	if p.Validate() != nil {
		t.Fatal("valid cursor")
	}
	p = historyFixture()
	p.Items = p.Items[1:]
	if p.Validate() != nil {
		t.Fatal("later page")
	}
	p = historyFixture()
	p.Items = p.Items[:1]
	p.Items[0].Kind = "observed"
	for len(p.Items) < 100 {
		a := p.Items[0]
		a.Sequence = int64(len(p.Items) + 1)
		p.Items = append(p.Items, a)
	}
	if p.Validate() != nil {
		t.Fatal("exact limit")
	}
	a := p.Items[0]
	a.Sequence = 101
	p.Items = append(p.Items, a)
	if p.Validate() == nil {
		t.Fatal("overlimit")
	}
}

func TestLeaseAttentionHistoryOptionsValidation(t *testing.T) {
	for _, after := range []int64{0, 1, math.MaxInt64 - 101} {
		for _, limit := range []int{1, 100} {
			if (LeaseAttentionHistoryOptions{AfterSequence: after, Limit: limit}).Validate() != nil {
				t.Fatal(after, limit)
			}
		}
	}
	for _, o := range []LeaseAttentionHistoryOptions{{Limit: 0}, {Limit: 101}, {Limit: 1, AfterSequence: -1}, {Limit: 1, AfterSequence: math.MaxInt64 - 100}} {
		if o.Validate() == nil {
			t.Fatal(o)
		}
	}
}
