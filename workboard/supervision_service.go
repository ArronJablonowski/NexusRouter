package workboard

import (
	"context"
	"time"
)

const MaxAttentionScanClaims = 100

type AttentionReason string

const (
	AttentionExpired AttentionReason = "lease_expired"
	AttentionStale   AttentionReason = "heartbeat_stale"
)

type ClaimAttention struct {
	Version       int             `json:"version"`
	BoardID       string          `json:"board_id"`
	CardID        string          `json:"card_id"`
	AttemptID     string          `json:"attempt_id"`
	ClaimID       string          `json:"claim_id"`
	ClaimRevision int64           `json:"claim_revision"`
	OwnerID       string          `json:"owner_id"`
	Reason        AttentionReason `json:"reason"`
	ObservedAt    time.Time       `json:"observed_at"`
	ExpiresAt     time.Time       `json:"expires_at"`
}

func (a ClaimAttention) Validate() error {
	if a.Version != SchemaVersion || !validLifecycleIDs(a.BoardID, a.CardID, a.AttemptID, a.ClaimID, a.OwnerID) ||
		a.ClaimRevision < 2 || a.Reason != AttentionExpired && a.Reason != AttentionStale || !validTime(a.ObservedAt) ||
		!validTime(a.ExpiresAt) || a.Reason == AttentionExpired && a.ObservedAt.Before(a.ExpiresAt) ||
		a.Reason == AttentionStale && !a.ObservedAt.Before(a.ExpiresAt) {
		return fail(CodeInvalid, "claim_attention")
	}
	return nil
}

type AttentionScan struct {
	BoardID     string
	Actor       Actor
	ObservedAt  time.Time
	StaleBefore time.Time
	Limit       int
}

func (o AttentionScan) Validate() error {
	if !validID(o.BoardID) || o.Actor.Validate() != nil || o.Actor.Type != "system" || !validTime(o.ObservedAt) ||
		!validTime(o.StaleBefore) || o.StaleBefore.After(o.ObservedAt) || o.Limit < 1 || o.Limit > MaxAttentionScanClaims {
		return fail(CodeInvalid, "attention_scan")
	}
	return nil
}

type AttentionRepository interface {
	ObserveClaimAttention(context.Context, AttentionScan) ([]ClaimAttention, error)
	ListClaimAttention(context.Context, string, int) ([]ClaimAttention, error)
}

type AttentionService struct {
	repository AttentionRepository
	authority  AuthoritySource
	now        func() time.Time
	staleAfter time.Duration
}

func NewAttentionService(repository AttentionRepository, authority AuthoritySource, now func() time.Time, staleAfter time.Duration) (*AttentionService, error) {
	if repository == nil || authority == nil || now == nil || staleAfter < MinLeaseTTL || staleAfter > MaxLeaseTTL {
		return nil, fail(CodeInvalid, "service")
	}
	return &AttentionService{repository: repository, authority: authority, now: now, staleAfter: staleAfter}, nil
}

func (s *AttentionService) Scan(ctx context.Context, boardID string, limit int) ([]ClaimAttention, error) {
	authority, err := s.authority.WorkboardAuthority(ctx)
	if err != nil || authority.Validate() != nil || authority.Actor.Type != "system" {
		return nil, fail(CodeInvalid, "authority")
	}
	now := s.now().UTC()
	observation := AttentionScan{BoardID: boardID, Actor: authority.Actor, ObservedAt: now, StaleBefore: now.Add(-s.staleAfter), Limit: limit}
	if observation.Validate() != nil {
		return nil, fail(CodeInvalid, "attention_scan")
	}
	return s.repository.ObserveClaimAttention(ctx, observation)
}

func (s *AttentionService) List(ctx context.Context, boardID string, limit int) ([]ClaimAttention, error) {
	authority, err := s.authority.WorkboardAuthority(ctx)
	if err != nil || authority.Validate() != nil || authority.Actor.Type != "system" && authority.Actor.Type != "operator" ||
		!validID(boardID) || limit < 1 || limit > MaxAttentionScanClaims {
		return nil, fail(CodeInvalid, "authority")
	}
	return s.repository.ListClaimAttention(ctx, boardID, limit)
}
