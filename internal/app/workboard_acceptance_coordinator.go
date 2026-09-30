package app

import (
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/workboard"
)

const configuredAcceptanceAuthority = "workboard-criterion-coordinator"

func newConfiguredWorkboardAcceptanceCoordinator(store *telemetry.Store, now func() time.Time) (*workboard.AcceptanceCoordinator, error) {
	if store == nil || now == nil {
		return nil, ErrAdmission
	}
	authority := fixedWorkboardAuthority{authority: workboard.Authority{CreationScope: configuredAcceptanceAuthority,
		Actor: workboard.Actor{ID: configuredAcceptanceAuthority, Type: "validator"}}}
	decisions, err := workboard.NewEvaluationService(store, authority, unavailableCandidateEvaluator{}, now)
	if err != nil {
		return nil, err
	}
	return workboard.NewAcceptanceCoordinator(store, decisions)
}
