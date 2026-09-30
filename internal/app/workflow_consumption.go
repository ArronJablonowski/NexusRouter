package app

import (
	"context"
	"encoding/hex"
	"strings"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/skills"
)

// ConsumeSkillWorkflowScan rechecks and groups one saved scan page. The expected
// revision is a retry key, not permission to skip unconsumed pages. This neither
// invokes a provider nor publishes or activates a skill.
func (s *Service) ConsumeSkillWorkflowScan(ctx context.Context, name string, expectedRevision int64) (skills.WorkflowScanConsumption, error) {
	bad := func() (skills.WorkflowScanConsumption, error) { return skills.WorkflowScanConsumption{}, ErrAdmission }
	if !s.skillScanAdmission(ctx, name) || !s.settings.Skills.AutoDraft || expectedRevision < 0 || expectedRevision >= 1_000_000_000 {
		return bad()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	scope := s.settings.Skills.Scope
	inputs := []string{scope, name}
	secrets := memorySecrets(s.settings, s.secret)
	if !selectionValueClean(inputs, secrets) {
		return bad()
	}
	db, err := telemetry.OpenWorkflowScanControl(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return bad()
	}
	defer db.Close()
	clean := func(receipt skills.WorkflowScanConsumption) bool {
		return receipt.Validate() == nil && receipt.Scope == scope && receipt.Name == name && receipt.Revision == expectedRevision+1 && selectionValueClean([]any{inputs, receipt}, secrets)
	}
	guard := func(guardCtx context.Context, receipt skills.WorkflowScanConsumption) error {
		secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
		if guardCtx.Err() != nil || !clean(receipt) {
			return ErrAdmission
		}
		return nil
	}
	receipt, err := db.ConsumeWorkflowScan(ctx, scope, name, expectedRevision, guard)
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if err != nil || ctx.Err() != nil || !clean(receipt) {
		return bad()
	}
	return receipt, nil
}

// SkillWorkflowScanConsumption inspects the last committed consumption receipt.
// Inspection remains available with automatic drafting disabled.
func (s *Service) SkillWorkflowScanConsumption(ctx context.Context, name string) (skills.WorkflowScanConsumption, error) {
	bad := func() (skills.WorkflowScanConsumption, error) { return skills.WorkflowScanConsumption{}, ErrAdmission }
	if !s.skillScanAdmission(ctx, name) {
		return bad()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	scope := s.settings.Skills.Scope
	inputs := []string{scope, name}
	secrets := memorySecrets(s.settings, s.secret)
	if !selectionValueClean(inputs, secrets) {
		return bad()
	}
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return bad()
	}
	defer db.Close()
	receipt, err := db.WorkflowScanConsumption(ctx, scope, name)
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if err != nil || receipt.Validate() != nil || receipt.Scope != scope || receipt.Name != name || !selectionValueClean([]any{inputs, receipt}, secrets) || ctx.Err() != nil {
		return bad()
	}
	return receipt, nil
}

// SkillWorkflowScanBuckets lists bounded, ordered grouping observations for one
// epoch, including singleton buckets. These are not fresh generation authority;
// selection planning must revalidate their source tasks before inference.
func (s *Service) SkillWorkflowScanBuckets(ctx context.Context, name string, epoch int64, afterID string, limit int) ([]skills.WorkflowBucket, error) {
	if !s.skillScanAdmission(ctx, name) || epoch < 1 || epoch > 1_000_000_000 || limit < 1 || limit > 20 || !workflowBucketCursor(afterID) {
		return nil, ErrAdmission
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	scope := s.settings.Skills.Scope
	inputs := []string{scope, name, afterID}
	secrets := memorySecrets(s.settings, s.secret)
	if !selectionValueClean(inputs, secrets) {
		return nil, ErrAdmission
	}
	db, err := telemetry.OpenReadOnly(ctx, s.settings.Telemetry.Database)
	if err != nil {
		return nil, ErrAdmission
	}
	defer db.Close()
	buckets, err := db.ListWorkflowScanBuckets(ctx, scope, name, epoch, afterID, limit)
	secrets = append(secrets, memorySecrets(s.settings, s.secret)...)
	if err != nil || buckets == nil || len(buckets) > limit || !selectionValueClean([]any{inputs, buckets}, secrets) || ctx.Err() != nil {
		return nil, ErrAdmission
	}
	previous := afterID
	for _, bucket := range buckets {
		if bucket.Validate() != nil || bucket.ID <= previous {
			return nil, ErrAdmission
		}
		previous = bucket.ID
	}
	return buckets, nil
}

func workflowBucketCursor(id string) bool {
	if id == "" {
		return true
	}
	_, err := hex.DecodeString(id)
	return len(id) == 64 && strings.ToLower(id) == id && err == nil
}
