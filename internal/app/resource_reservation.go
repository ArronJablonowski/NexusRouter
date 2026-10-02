package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/branding"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/internal/hostresources"
	"github.com/ArronJablonowski/NexusRouter/internal/processguard"
	"github.com/ArronJablonowski/NexusRouter/resources"
)

const (
	hostResourceScope        = "darwin-local-host-v1"
	hostResourceTTL          = 30 * time.Second
	hostResourceRenewTimeout = 5 * time.Second
)

type resourceReservationInput struct {
	reservationID string
	taskID        string
	sessionID     string
	profile       string
	contextTokens int
}

// InstallHostResourceCoordinator enables durable host-wide admission for one
// daemon Service. Ordinary NewService users retain the existing process-local
// Budget fast path. Installation must finish before any local reservation.
func InstallHostResourceCoordinator(ctx context.Context, service *Service, daemonID string) (func() error, error) {
	if ctx == nil || service == nil || !reservationLabel(daemonID, 128) {
		return nil, ErrAdmission
	}
	reference, err := processguard.Current(ctx)
	if err != nil || reference.Validate() != nil {
		return nil, ErrAdmission
	}
	var coordinator *hostresources.Coordinator
	// Native process fixtures already isolate the process-owner root. Keep the
	// associated host coordinator in that same private root unless its own path
	// was explicitly selected; this also makes multiple fixture daemons sharing
	// one owner root contend in the same deterministic host scope.
	ownerRoot := branding.Getenv("DARWIN_PROCESS_OWNER_DIR")
	if branding.Getenv("DARWIN_RESOURCE_COORDINATOR_DB") == "" && filepath.IsAbs(ownerRoot) && filepath.Clean(ownerRoot) == ownerRoot {
		path := filepath.Join(ownerRoot, "host-resources.db")
		if service.settings.Hardware.Concurrent == "auto" {
			coordinator, err = hostresources.OpenPathAdaptive(ctx, path, service.resourceLimits)
		} else {
			coordinator, err = hostresources.OpenPath(ctx, path, service.resourceLimits)
		}
	} else if service.settings.Hardware.Concurrent == "auto" {
		coordinator, err = hostresources.OpenAdaptive(ctx, service.resourceLimits)
	} else {
		coordinator, err = hostresources.Open(ctx, service.resourceLimits)
	}
	if err != nil {
		return nil, ErrAdmission
	}
	if _, err = coordinator.RecoverStopped(ctx, time.Now().UTC()); err != nil {
		_ = coordinator.Close()
		return nil, ErrAdmission
	}
	closeCoordinator, err := installResourceCoordinator(service, coordinator, resources.ReservationOwner{ProcessID: reference.ID, DaemonID: daemonID}, coordinator.Close)
	if err != nil {
		_ = coordinator.Close()
		return nil, err
	}
	return closeCoordinator, nil
}

func installResourceCoordinator(service *Service, coordinator resources.Coordinator, owner resources.ReservationOwner, closeCoordinator func() error) (func() error, error) {
	if service == nil || coordinator == nil || owner.Validate() != nil || closeCoordinator == nil {
		return nil, ErrAdmission
	}
	service.resourceCoordinatorMu.Lock()
	defer service.resourceCoordinatorMu.Unlock()
	if service.resourceCoordinator != nil || service.resourceReservationStarted {
		return nil, ErrAdmission
	}
	service.resourceCoordinator = coordinator
	service.resourceOwner = owner
	var once sync.Once
	var closeErr error
	return func() error {
		once.Do(func() {
			if err := closeCoordinator(); err != nil {
				closeErr = ErrAdmission
			}
		})
		return closeErr
	}, nil
}

func (s *Service) coordinatedResources() (resources.Coordinator, resources.ReservationOwner) {
	s.resourceCoordinatorMu.Lock()
	defer s.resourceCoordinatorMu.Unlock()
	s.resourceReservationStarted = true
	return s.resourceCoordinator, s.resourceOwner
}

func (s *Service) reservePrimary(ctx, admission context.Context, model config.Model, request Request) (context.Context, func() error, error) {
	sized, sizeErr := contextReservationModel(model, request.ContextTokens)
	if sizeErr != nil {
		return ctx, nil, sizeErr
	}
	model = sized
	coordinator, owner := s.coordinatedResources()
	if coordinator == nil {
		release, err := s.reserveExplicitLocal(admission, model)
		if err != nil {
			return ctx, nil, err
		}
		return ctx, func() error { release(); return nil }, nil
	}
	profile := request.Profile
	if profile == "" {
		profile = "default"
	}
	contextTokens := request.ContextTokens
	if contextTokens == 0 {
		contextTokens = model.WorkingContextTokens()
	}
	taskID, sessionID := request.taskID, request.sessionID
	if request.runtimeHostAdmission != nil {
		taskID = request.runtimeHostAdmission.taskID
		sessionID = request.runtimeHostAdmission.sessionID
	}
	input := resourceReservationInput{
		reservationID: reservationIdentity(taskID, model.ID, owner.DaemonID),
		taskID:        taskID,
		sessionID:     sessionID,
		profile:       profile,
		contextTokens: contextTokens,
	}
	return s.reserveCoordinated(admission, ctx, coordinator, owner, model, input)
}

// reserveAuxiliary preserves the legacy release-only admission contract.
// Production auxiliary inference must use reserveAuxiliaryExecution so lease
// loss cancels the provider call and can prevent a successful result.
func (s *Service) reserveAuxiliary(ctx context.Context, model config.Model) (func(), error) {
	_, release, err := s.reserveAuxiliaryExecution(ctx, model)
	if err != nil {
		return nil, err
	}
	return func() { _ = release() }, nil
}

func (s *Service) reserveAuxiliaryExecution(ctx context.Context, model config.Model) (context.Context, func() error, error) {
	coordinator, owner := s.coordinatedResources()
	if coordinator == nil {
		release, err := s.reserveExplicitLocal(ctx, model)
		if err != nil {
			return ctx, nil, err
		}
		return ctx, func() error { release(); return nil }, nil
	}
	id := rand.Text()
	contextTokens := model.WorkingContextTokens()
	if contextTokens < 1 {
		return ctx, nil, ErrAdmission
	}
	run, release, err := s.reserveCoordinated(ctx, ctx, coordinator, owner, model, resourceReservationInput{
		reservationID: id,
		taskID:        "aux-" + id,
		sessionID:     "aux-" + id,
		profile:       "auxiliary",
		contextTokens: contextTokens,
	})
	if err != nil {
		return ctx, nil, err
	}
	return run, release, nil
}

// auxiliaryCleanup closes the provider before releasing its durable host
// reservation. It is safe to defer and then call explicitly before publishing
// a successful result; the first call returns any lease/release failure.
func auxiliaryCleanup(closeProvider func(), release func() error) func() error {
	var once sync.Once
	var err error
	return func() error {
		once.Do(func() {
			if closeProvider != nil {
				closeProvider()
			}
			if release != nil {
				err = release()
			}
		})
		return err
	}
}

func (s *Service) reserveCoordinated(admission, execution context.Context, coordinator resources.Coordinator, owner resources.ReservationOwner, model config.Model, input resourceReservationInput) (context.Context, func() error, error) {
	if admission == nil || execution == nil || coordinator == nil || owner.Validate() != nil ||
		!reservationLabel(input.reservationID, 128) || !reservationLabel(input.taskID, 128) ||
		!reservationLabel(input.sessionID, 128) || !reservationLabel(input.profile, 128) || input.contextTokens < 1 {
		return execution, nil, ErrAdmission
	}
	configDigest, err := settingsConfigID(s.settings)
	if err != nil {
		return execution, nil, ErrAdmission
	}
	warmRAM, warmObserved := s.warmMemoryEstimate(admission, model, input.contextTokens)
	if err = s.lockResources(admission); err != nil {
		return execution, nil, err
	}
	locked := true
	defer func() {
		if locked {
			s.mu.Unlock()
		}
	}()
	snapshot, err := s.resourceProfile(admission)
	if err != nil {
		return execution, nil, ErrAdmission
	}
	now := s.routingNow()
	coldRAM := uint64(0)
	verifier, canFence := coordinator.(warmMemoryCoordinator)
	if canFence && snapshot.UnifiedMemory && warmRAM > 0 && !now.Before(warmObserved) && now.Sub(warmObserved) <= time.Second {
		coldRAM = model.RAMBytes
		model.RAMBytes = warmRAM
	}
	// Do not call reserveExplicitLocal from the coordinated path: its managed
	// residency branch has only process-local active/uncertain maps and could
	// unload a peer daemon's model. Coordinated daemons conservatively retain
	// resident models until residency lifecycle ownership is also durable.
	request := resources.ReservationRequest{
		Version: resources.ReservationContractVersion, ReservationID: input.reservationID,
		HostScope: hostResourceScope, Owner: owner, TaskID: input.taskID, SessionID: input.sessionID,
		ProviderID: model.Provider, ModelID: model.ID, Profile: input.profile, GPUDevice: model.GPUDevice,
		RAMBytes: model.RAMBytes, VRAMBytes: model.VRAMBytes, ContextTokens: input.contextTokens,
		ConfigDigest: configDigest, RequestedAt: now, TTL: s.resourceReservationTTL,
	}
	if coldRAM != 0 {
		request.ColdRAMBytes, request.ResidencyDigest = coldRAM, model.ResidencyDigest
	}
	// Hold the process-local fast-path reservation while attempting the durable
	// claim. A durable failure therefore cannot create local overlap, while a
	// local capacity denial creates no terminal durable tombstone for a pressure
	// retry with the same frozen identity.
	localRelease, err := s.budget.Reserve(snapshot, modelResources(model), now)
	if err != nil {
		if errors.Is(err, resources.ErrCapacity) {
			return execution, nil, resources.ErrCapacity
		}
		return execution, nil, ErrAdmission
	}
	var binding resources.ReservationBinding
	if coldRAM != 0 {
		coldModel := model
		coldModel.RAMBytes = coldRAM
		binding, err = verifier.AcquireVerifiedWarm(admission, snapshot, request, now, func(ctx context.Context) (resources.Snapshot, time.Time, error) {
			amount, observed := s.warmMemoryEstimate(ctx, coldModel, input.contextTokens)
			if amount != warmRAM {
				return resources.Snapshot{}, time.Time{}, resources.ErrCapacity
			}
			fresh, e := s.resourceProfile(ctx)
			at := s.routingNow()
			if e != nil || !fresh.UnifiedMemory || at.Before(observed) || at.Sub(observed) > time.Second {
				return resources.Snapshot{}, time.Time{}, resources.ErrCapacity
			}
			return fresh, at, nil
		})
	} else {
		binding, err = coordinator.Acquire(admission, snapshot, request, now)
	}
	if err != nil {
		localRelease()
		if errors.Is(err, resources.ErrCapacity) {
			return execution, nil, resources.ErrCapacity
		}
		return execution, nil, ErrAdmission
	}
	// Fenced observation can advance acquisition time; inspect with a fresh clock.
	now = s.routingNow()
	digest, digestErr := request.CanonicalDigest()
	status, statusErr := coordinator.Status(admission, request.ReservationID, now)
	if binding.Validate() != nil || digestErr != nil || binding.RequestDigest != digest ||
		binding.Request.ReservationID != request.ReservationID || binding.Request.Owner != owner ||
		statusErr != nil || status.Validate() != nil || status.State != resources.ReservationActive || status.RequestDigest != digest {
		localRelease()
		s.mu.Unlock()
		locked = false
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(admission), 5*time.Second)
		_, _ = coordinator.Release(releaseCtx, request.ReservationID, owner, s.routingNow())
		cancel()
		return execution, nil, ErrAdmission
	}
	s.mu.Unlock()
	locked = false

	run, cancelRun := context.WithCancel(execution)
	stop := make(chan struct{})
	done := make(chan struct{})
	var lost bool
	var lostMu sync.Mutex
	go func() {
		defer close(done)
		interval := s.resourceReservationTTL / 3
		renewTimeout := min(s.resourceRenewTimeout, interval)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				renewCtx, cancel := context.WithTimeout(context.WithoutCancel(execution), renewTimeout)
				_, renewErr := coordinator.Renew(renewCtx, request.ReservationID, owner, s.routingNow(), s.resourceReservationTTL)
				cancel()
				if renewErr != nil {
					lostMu.Lock()
					lost = true
					lostMu.Unlock()
					cancelRun()
					return
				}
			}
		}
	}()

	var once sync.Once
	var releaseErr error
	release := func() error {
		once.Do(func() {
			close(stop)
			<-done
			cancelRun()
			localRelease()
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(execution), 5*time.Second)
			_, durableErr := coordinator.Release(releaseCtx, request.ReservationID, owner, s.routingNow())
			cancel()
			lostMu.Lock()
			wasLost := lost
			lostMu.Unlock()
			if wasLost || durableErr != nil {
				releaseErr = ErrAdmission
			}
		})
		return releaseErr
	}
	return run, release, nil
}

func reservationIdentity(taskID, modelID, daemonID string) string {
	digest := sha256.Sum256([]byte("darwin.app.resource-reservation.v1\x00" + taskID + "\x00" + modelID + "\x00" + daemonID))
	return hex.EncodeToString(digest[:])
}

func reservationLabel(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, char := range value {
		if char < 0x21 || char > 0x7e {
			return false
		}
	}
	return true
}

func (s *Service) hostReservationSnapshot(ctx context.Context, now time.Time) (resources.ReservationSnapshot, bool, error) {
	if s == nil || ctx == nil {
		return resources.ReservationSnapshot{}, false, ErrAdmission
	}
	s.resourceCoordinatorMu.Lock()
	coordinator := s.resourceCoordinator
	s.resourceCoordinatorMu.Unlock()
	if coordinator == nil {
		return resources.ReservationSnapshot{}, false, nil
	}
	snapshot, err := coordinator.Snapshot(ctx, now.UTC())
	if err != nil || snapshot.Validate() != nil {
		return resources.ReservationSnapshot{}, true, ErrAdmission
	}
	return snapshot, true, nil
}
