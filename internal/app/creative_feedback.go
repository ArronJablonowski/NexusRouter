package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/ArronJablonowski/NexusRouter/accounting"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/sessions"
)

// Categories are inferred from the original request, never from model claims
// about its own quality. Generic requests do not acquire creative preferences.
func creativeKind(prompt string) string {
	p := " " + strings.Join(strings.FieldsFunc(strings.ToLower(prompt), func(r rune) bool { return !(r >= 'a' && r <= 'z') }), " ")
	for _, group := range []struct {
		kind  string
		words []string
	}{
		{"image", []string{"image", "illustration", "picture", "comfyui"}},
		{"poetry", []string{"poem", "poetry", "sonnet", "lyrics"}},
		{"fiction", []string{"story", "fiction", "novel", "screenplay"}},
		{"marketing", []string{"marketing", "advertis", "sales copy", "tagline"}},
		{"design", []string{"design", "concept", "creative", "brainstorm"}},
		{"document", []string{"document", "report", "article", "essay", "brief"}},
	} {
		for _, word := range group.words {
			if strings.Contains(p, " "+word) {
				return group.kind
			}
		}
	}
	return "general"
}

func (b *BrowserMutations) fillCreativeFeedback(ctx context.Context, db *telemetry.Store, snapshot sessions.Snapshot, entry *telemetry.BrowserFeedback, rating int) error {
	entry.Rating = rating
	entry.CostStatus = "unknown"
	entry.AttemptCost = 0 // Placeholder only; CostStatus prevents interpreting it as measured zero.
	prompt := ""
	for _, m := range snapshot.Messages {
		if m.Role == "user" {
			prompt = m.Content
		}
	}
	entry.CreativeKind = creativeKind(prompt)
	var after int64
	for pages := 0; pages < 1000; pages++ {
		events, err := db.Read(ctx, snapshot.TaskID, after, 256)
		if err != nil {
			return err
		}
		for _, e := range events {
			after = e.Sequence
			if e.Kind == runtime.TurnStarted {
				entry.Model, entry.Provider = e.Data.ModelID, e.Data.ProviderID
			}
		}
		if len(events) < 256 {
			break
		}
		if pages == 999 {
			return ErrAdmission
		}
	}
	if entry.Model == "" || entry.Provider == "" {
		return ErrAdmission
	}
	entry.AttemptCost, entry.CostStatus = creativeTaskCost(ctx, db, snapshot.TaskID)
	return nil
}

func creativeTaskCost(ctx context.Context, db *telemetry.Store, task string) (float64, string) {
	totals, err := db.UsageTotals(ctx, accounting.Scope{TaskID: task})
	if err == nil && totals.Coverage == accounting.CompleteCoverage && totals.Overall.NormalizedCost != nil {
		return *totals.Overall.NormalizedCost, "estimate"
	}
	return 0, "unknown"
}

func creativeCostSummary(r telemetry.BrowserFeedback) string {
	if r.CostStatus == "unknown" {
		return "Model cost unavailable: usage or pricing is incomplete. No cost input is needed."
	}
	return fmt.Sprintf("Recorded task model cost estimate: $%.6f USD. Not an invoice; excludes electricity and unmetered external tools.", r.AttemptCost)
}

func creativeRequestKind(r Request) string {
	if r.Prompt != "" {
		return creativeKind(r.Prompt)
	}
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == "user" {
			return creativeKind(r.Messages[i].Content)
		}
	}
	return "general"
}
