package app

import (
	"context"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

func (s *Service) skillScanAdmission(ctx context.Context, name string) bool {
	return s != nil && ctx != nil && ctx.Err() == nil && s.settings.Validate() == nil && s.settings.Skills.Enabled && s.settings.Skills.Root != "" && skillGenerationIdentifier.MatchString(s.settings.Skills.Scope) && skillGenerationIdentifier.MatchString(name)
}

// SkillWorkflowScan inspects current progress without creating or migrating
// storage. Disabling automatic drafting does not disable operator inspection.
func (s *Service) SkillWorkflowScan(ctx context.Context, name string) (skills.WorkflowScan, error) {
	bad := func() (skills.WorkflowScan, error) { return skills.WorkflowScan{}, ErrAdmission }
	if !s.skillScanAdmission(ctx, name) {
		return bad()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	scope := s.settings.Skills.Scope
	secrets := memorySecrets(s.settings, s.secret)
	if !selectionValueClean([]string{scope, name}, secrets) {
		return bad()
	}
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return bad()
	}
	defer db.Close()
	scan, err := db.WorkflowScan(ctx, scope, name)
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if err != nil || scan.Validate() != nil || scan.Scope != scope || scan.Name != name || !selectionValueClean(scan, secrets) || ctx.Err() != nil {
		return bad()
	}
	return scan, nil
}

// AdvanceSkillWorkflowScan commits one admitted discovery page, not inference.
// The exact revision/limit is its retry key: a lost response does not authorize
// skipping ahead. Metadata is checked before persistence without rewriting IDs.
// Scope is destination configuration, not source-project or tenant ownership.
func (s *Service) AdvanceSkillWorkflowScan(ctx context.Context, name, domain string, expectedRevision int64, scanLimit int) (skills.WorkflowScanPage, error) {
	bad := func() (skills.WorkflowScanPage, error) { return skills.WorkflowScanPage{}, ErrAdmission }
	if !s.skillScanAdmission(ctx, name) || !s.settings.Skills.AutoDraft || !skillGenerationIdentifier.MatchString(domain) || expectedRevision < 0 || expectedRevision >= 1_000_000_000 || scanLimit < 1 || scanLimit > 20 {
		return bad()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	scope := s.settings.Skills.Scope
	secrets := memorySecrets(s.settings, s.secret)
	inputs := []string{scope, name, domain}
	if !selectionValueClean(inputs, secrets) {
		return bad()
	}
	db, err := telemetry.OpenWorkflowScanControl(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return bad()
	}
	defer db.Close()
	guard := func(guardCtx context.Context, page skills.WorkflowScanPage) error {
		secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
		if guardCtx.Err() != nil || page.Validate() != nil || page.Scan.Scope != scope || page.Scan.Name != name || page.Scan.Domain != domain || page.Scan.Revision != expectedRevision+1 || page.Limit != scanLimit || !selectionValueClean([]any{inputs, page}, secrets) {
			return ErrAdmission
		}
		return nil
	}
	page, err := db.AdvanceWorkflowScanGuarded(ctx, scope, name, domain, expectedRevision, scanLimit, guard)
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if err != nil || page.Validate() != nil || page.Scan.Scope != scope || page.Scan.Name != name || page.Scan.Domain != domain || page.Scan.Revision != expectedRevision+1 || page.Limit != scanLimit || !selectionValueClean([]any{inputs, page}, secrets) || ctx.Err() != nil {
		return bad()
	}
	return page, nil
}
