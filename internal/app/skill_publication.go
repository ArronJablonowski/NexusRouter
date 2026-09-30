package app

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ArronJablonowski/NexusRouter/skills"
)

// PublishSkillGeneration copies an inspected durable proposal into the configured
// file catalog as an inactive version. It neither revalidates source freshness
// nor activates the workflow, and never mutates an injected retrieval store.
func (s *Service) PublishSkillGeneration(ctx context.Context, attemptID string) (skills.Version, error) {
	bad := func() (skills.Version, error) { return skills.Version{}, ErrAdmission }
	if s == nil || ctx == nil || s.settings.Validate() != nil || !s.settings.Skills.Enabled || !s.settings.Skills.AutoDraft || s.settings.Skills.Root == "" || s.settings.Skills.Scope == "" || s.skillStore != nil || !skillGenerationIdentifier.MatchString(attemptID) {
		return bad()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return bad()
	}
	a, err := InspectSkillGeneration(ctx, s.settings.Telemetry.Database, s.settings.Skills.Scope, attemptID)
	if err != nil || a.Validate() != nil || a.Status != "drafted" || a.Result == nil {
		return bad()
	}
	// A changed credential must not silently rewrite a previously bound proposal.
	// Check raw strings too: JSON escaping can conceal a multiline secret.
	secrets := memorySecrets(s.settings, s.secret)
	body, err := json.Marshal(a)
	if err != nil || redact(string(body), secrets) != string(body) {
		return bad()
	}
	d := a.Result.Draft
	values := []string{a.ID, a.Key.Scope, a.Key.Name, a.Model, a.Provider, a.InputDigest, a.Result.Model, d.Description, d.Configuration}
	for _, list := range [][]string{a.SourceSessions, a.SourceEvidence, d.SourceSessions, d.SourceEvidence, d.Tags, d.Steps, d.RequiredTools, d.Risks, d.ValidationCases} {
		values = append(values, list...)
	}
	for _, value := range values {
		if redact(value, secrets) != value {
			return bad()
		}
	}
	if ctx.Err() != nil {
		return bad()
	}
	store, err := skills.Open(s.settings.Skills.Root, []string{s.settings.Skills.Scope})
	if err != nil {
		return bad()
	}
	defer store.Close()
	store.SetAutomatic(true)
	version, err := store.PublishGeneration(ctx, a, true)
	if err != nil {
		return bad()
	}
	return version, nil
}
