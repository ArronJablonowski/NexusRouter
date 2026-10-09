package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/tools"
)

// This catalogue describes authority; it does not open roots, databases or invoke
// handlers. Execution independently builds the ordinary registry and compares it.
type nativeToolContract struct {
	Catalog  []providers.Tool
	MaxTurns int
	Rules    []tools.Rule
}

func nativeToolsFor(s config.Settings, extension *tools.Extension) nativeToolContract {
	c := nativeToolContract{MaxTurns: min(s.Runtime.MaxTurns, s.Tools.MaxTurns), Rules: extension.Rules()}
	if s.Tools.CollaborationEnabled {
		c.Catalog = append(c.Catalog, modelCollaborationSpecs()...)
	}
	if s.Tools.Enabled {
		c.Catalog = append(c.Catalog, readFileSpec())
	}
	if s.Tools.CreateEnabled {
		c.Catalog = append(c.Catalog, createFileSpec())
	}
	if s.Tools.ReplaceEnabled {
		c.Catalog = append(c.Catalog, replaceFileSpec())
	}
	if s.Tools.WorkboardReadEnabled {
		c.Catalog = append(c.Catalog, workboardListSpec(), workboardReadSpec())
	}
	if s.Tools.WorkboardWriteEnabled {
		c.Catalog = append(c.Catalog, workboardMutationSpecs()...)
		c.Catalog = append(c.Catalog, workboardAgentProposalSpecs()...)
	}
	c.Catalog = append(c.Catalog, extension.Catalog()...)
	sort.Slice(c.Catalog, func(i, j int) bool { return c.Catalog[i].Name < c.Catalog[j].Name })
	return c
}
func (c nativeToolContract) policyDigest(base string) string {
	body, _ := json.Marshal(struct {
		Base  string
		Rules []tools.Rule
	}{base, c.Rules})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
