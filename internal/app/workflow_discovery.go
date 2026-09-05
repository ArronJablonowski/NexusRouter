package app

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/sessions"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

// DiscoverSkillWorkflows inspects bounded task metadata for learning candidates.
// It does not run a model, initialize storage, open the skill root, or mutate an
// injected retrieval store. Candidates still require admission at generation;
// discovery is not approval to publish or activate a workflow.
func (s *Service) DiscoverSkillWorkflows(ctx context.Context, domain, after string, scanLimit int) (skills.WorkflowCandidatePage, error) {
	bad := func() (skills.WorkflowCandidatePage, error) { return skills.WorkflowCandidatePage{}, ErrAdmission }
	if s == nil || ctx == nil || s.settings.Validate() != nil || !s.settings.Skills.Enabled || !s.settings.Skills.AutoDraft || s.settings.Skills.Root == "" || !skillGenerationIdentifier.MatchString(s.settings.Skills.Scope) || !skillGenerationIdentifier.MatchString(domain) || (after != "" && !sessions.ValidEventPageID(after)) || scanLimit < 1 || scanLimit > 20 {
		return bad()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return bad()
	}
	secrets := memorySecrets(s.settings, s.secret)
	for _, input := range []string{domain, after, s.settings.Skills.Scope} {
		if redact(input, secrets) != input {
			return bad()
		}
	}
	store, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return bad()
	}
	defer store.Close()
	page, err := store.DiscoverSkillWorkflows(ctx, domain, after, scanLimit)
	if err != nil || page.Domain != domain || page.Validate(after, scanLimit) != nil || ctx.Err() != nil {
		return bad()
	}
	observedSecrets := append(append([]string(nil), secrets...), memorySecrets(s.settings, s.secret)...)
	for _, input := range []string{domain, after, s.settings.Skills.Scope} {
		if redact(input, observedSecrets) != input {
			return bad()
		}
	}
	if !workflowDiscoveryClean(page, observedSecrets) || ctx.Err() != nil {
		return bad()
	}
	return page, nil
}

// Inspect decoded strings too: JSON escaping must not conceal a credential in
// an identifier. Reject the whole page rather than rewriting cursor identities.
func workflowDiscoveryClean(page skills.WorkflowCandidatePage, secrets []string) bool {
	body, err := json.Marshal(page)
	if err != nil || redact(string(body), secrets) != string(body) {
		return false
	}
	var decoded any
	if json.Unmarshal(body, &decoded) != nil {
		return false
	}
	var clean func(any) bool
	clean = func(value any) bool {
		switch item := value.(type) {
		case string:
			return redact(item, secrets) == item
		case []any:
			for _, child := range item {
				if !clean(child) {
					return false
				}
			}
		case map[string]any:
			for key, child := range item {
				if redact(key, secrets) != key || !clean(child) {
					return false
				}
			}
		}
		return true
	}
	return clean(decoded)
}
