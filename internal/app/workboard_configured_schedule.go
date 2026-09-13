package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/internal/telemetry"
	"github.com/ArronJablonowski/DarwinRouter/workboard"
	"github.com/ArronJablonowski/DarwinRouter/workers"
)

const configuredWorkboardSchedulePolicyVersion = 1

// ConfiguredWorkboardSchedulePlan is an inert, fully composed scheduling plan.
// Preparation may inspect configuration and credentials, but opens no provider,
// starts no goroutine and mutates no durable state. Start may succeed only once.
type ConfiguredWorkboardSchedulePlan struct {
	mu        sync.Mutex
	started   bool
	lister    WorkboardBoardLister
	scheduler WorkboardCycleRunner
	interval  time.Duration
}

// PrepareConfiguredWorkboardSchedule binds unattended execution entirely from
// the Service's immutable configuration snapshot and an already-owned store.
func PrepareConfiguredWorkboardSchedule(ctx context.Context, service *Service,
	store *telemetry.Store,
) (*ConfiguredWorkboardSchedulePlan, error) {
	return prepareConfiguredWorkboardSchedule(ctx, service, store, time.Now)
}

func prepareConfiguredWorkboardSchedule(ctx context.Context, service *Service,
	store *telemetry.Store, now func() time.Time,
) (plan *ConfiguredWorkboardSchedulePlan, err error) {
	defer func() {
		if recover() != nil {
			plan, err = nil, ErrAdmission
		}
	}()
	if ctx == nil || ctx.Err() != nil || service == nil || store == nil || now == nil || service.settings.Validate() != nil ||
		!service.settings.Workboard.Scheduler.Enabled {
		return nil, ErrAdmission
	}
	heartbeat, heartbeatErr := config.Duration(service.settings.Workers.Heartbeat)
	lease, leaseErr := config.Duration(service.settings.Workers.Lease)
	interval, intervalErr := config.Duration(service.settings.Workboard.Scheduler.Interval)
	policyDigest, policyErr := configuredWorkboardSchedulePolicyDigest(service.settings)
	if heartbeatErr != nil || leaseErr != nil || intervalErr != nil || policyErr != nil {
		return nil, ErrAdmission
	}
	factory, err := service.ConfiguredWorkboardTaskFactory(ctx, store)
	if err != nil {
		return nil, ErrAdmission
	}
	reviewer, err := service.ConfiguredWorkboardCandidateReviewer(ctx)
	if err != nil {
		return nil, ErrAdmission
	}
	evaluator, err := newConfiguredWorkboardCandidateEvaluator(store, reviewer)
	if err != nil {
		return nil, ErrAdmission
	}
	workerSupervisor, err := workers.New(service.settings.Workers.Max, heartbeat, lease, store, store)
	if err != nil {
		return nil, ErrAdmission
	}
	runner, err := NewWorkboardWorkerRunner(workerSupervisor, store, evaluator, policyDigest, heartbeat, lease, now)
	if err != nil {
		return nil, ErrAdmission
	}
	coordinator, err := newConfiguredWorkboardAcceptanceCoordinator(store, now)
	if err != nil {
		return nil, ErrAdmission
	}
	runner.acceptance = coordinator
	authority := fixedWorkboardAuthority{authority: workboard.Authority{CreationScope: "workboard-scheduler", Actor: workboard.Actor{ID: "workboard-scheduler", Type: "system"}}}
	supervision, err := workboard.NewSupervisionService(store, authority, now, lease)
	if err != nil {
		return nil, ErrAdmission
	}
	scheduler, err := NewWorkboardScheduler(supervision, factory, runner, WorkboardScheduleLimits{
		MaxInFlight: service.settings.Workboard.Scheduler.MaxActiveClaims,
		ScanLimit:   service.settings.Workboard.Scheduler.CardScanLimit,
	})
	if err != nil {
		return nil, ErrAdmission
	}
	scheduler.acceptance = coordinator
	return &ConfiguredWorkboardSchedulePlan{lister: store, scheduler: scheduler, interval: interval}, nil
}

// Start transfers repeated scheduling ownership to one interval supervisor.
func (p *ConfiguredWorkboardSchedulePlan) Start(ctx context.Context) (*WorkboardScheduleSupervisor, error) {
	if p == nil || ctx == nil || ctx.Err() != nil {
		return nil, ErrAdmission
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started || p.lister == nil || p.scheduler == nil || p.interval <= 0 {
		return nil, ErrAdmission
	}
	supervisor, err := StartWorkboardScheduleSupervisor(ctx, p.lister, p.scheduler, p.interval)
	if err != nil {
		return nil, err
	}
	p.started = true
	return supervisor, nil
}

// configuredWorkboardSchedulePolicyDigest is domain-separated and versioned so
// durable claims cannot silently inherit changed host scheduling semantics. The
// settings digest is credential-free and conservatively invalidates all plans
// when any validated configuration changes.
func configuredWorkboardSchedulePolicyDigest(settings config.Settings) (string, error) {
	configID, err := settingsConfigID(settings)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(struct {
		Version  int    `json:"version"`
		ConfigID string `json:"config_id"`
	}{Version: configuredWorkboardSchedulePolicyVersion, ConfigID: configID})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("darwin.workboard.schedule.policy\x00"), body...))
	return hex.EncodeToString(digest[:]), nil
}
