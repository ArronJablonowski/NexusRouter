package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"github.com/ArronJablonowski/NexusRouter/resources"
)

type reservationCoordinatorFixture struct {
	mu        sync.Mutex
	requests  []resources.ReservationRequest
	releases  int
	renew     func(context.Context) error
	onRelease func() error
	active    map[string]resources.ReservationBinding
}

func newReservationCoordinatorFixture() *reservationCoordinatorFixture {
	return &reservationCoordinatorFixture{active: map[string]resources.ReservationBinding{}}
}

func (f *reservationCoordinatorFixture) Acquire(_ context.Context, _ resources.Snapshot, request resources.ReservationRequest, now time.Time) (resources.ReservationBinding, error) {
	digest, err := request.CanonicalDigest()
	if err != nil {
		return resources.ReservationBinding{}, err
	}
	binding := resources.ReservationBinding{Version: 1, Request: request, RequestDigest: digest, AcquiredAt: now.UTC(), ExpiresAt: now.Add(request.TTL).UTC()}
	f.mu.Lock()
	f.requests = append(f.requests, request)
	f.active[request.ReservationID] = binding
	f.mu.Unlock()
	return binding, nil
}

func (f *reservationCoordinatorFixture) Renew(ctx context.Context, id string, _ resources.ReservationOwner, _ time.Time, _ time.Duration) (resources.ReservationBinding, error) {
	if f.renew != nil {
		if err := f.renew(ctx); err != nil {
			return resources.ReservationBinding{}, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active[id], nil
}

func (f *reservationCoordinatorFixture) Release(_ context.Context, id string, _ resources.ReservationOwner, now time.Time) (resources.ReservationStatus, error) {
	if f.onRelease != nil {
		if err := f.onRelease(); err != nil {
			return resources.ReservationStatus{}, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	binding, ok := f.active[id]
	if !ok {
		return resources.ReservationStatus{}, resources.ErrReservation
	}
	delete(f.active, id)
	f.releases++
	return reservationStatusFixture(binding, resources.ReservationReleased, now), nil
}

func TestAuxiliaryReservationCancelsOnRenewalLossAndClosesProviderFirst(t *testing.T) {
	service, cfg := autoFixture(t)
	fixture := newReservationCoordinatorFixture()
	fixture.renew = func(context.Context) error { return resources.ErrReservationExpired }
	closed := false
	fixture.onRelease = func() error {
		if !closed {
			return errors.New("provider still open")
		}
		return nil
	}
	installReservationFixture(t, service, fixture)
	service.resourceReservationTTL = time.Second
	service.resourceRenewTimeout = 100 * time.Millisecond

	run, release, err := service.reserveAuxiliaryExecution(context.Background(), cfg.Models[0])
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-run.Done():
		if !errors.Is(run.Err(), context.Canceled) {
			t.Fatal("unexpected derived context error", run.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("renewal loss did not cancel auxiliary execution")
	}
	cleanup := auxiliaryCleanup(func() { closed = true }, release)
	if err = cleanup(); !errors.Is(err, ErrAdmission) {
		t.Fatal("renewal loss did not reject auxiliary success", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.releases != 1 || len(fixture.active) != 0 {
		t.Fatal("auxiliary reservation leaked", fixture.releases, len(fixture.active))
	}
}

func (f *reservationCoordinatorFixture) Status(_ context.Context, id string, now time.Time) (resources.ReservationStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	binding, ok := f.active[id]
	if !ok {
		return resources.ReservationStatus{}, resources.ErrReservation
	}
	return reservationStatusFixture(binding, resources.ReservationActive, now), nil
}

func (f *reservationCoordinatorFixture) Snapshot(_ context.Context, now time.Time) (resources.ReservationSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	snapshot := resources.ReservationSnapshot{Version: 1, ObservedAt: now.UTC(), DevicePools: []resources.ReservationPoolSnapshot{}}
	for _, binding := range f.active {
		snapshot.Active++
		snapshot.RAMBytes += binding.Request.RAMBytes
		snapshot.AggregateVRAMBytes += binding.Request.VRAMBytes
	}
	return snapshot, nil
}

func reservationStatusFixture(binding resources.ReservationBinding, state resources.ReservationState, now time.Time) resources.ReservationStatus {
	return resources.ReservationStatus{
		Version: 1, RequestDigest: binding.RequestDigest, State: state,
		RAMBytes: binding.Request.RAMBytes, VRAMBytes: binding.Request.VRAMBytes,
		ContextTokens: binding.Request.ContextTokens, DeviceBound: binding.Request.GPUDevice != "",
		AcquiredAt: binding.AcquiredAt, ExpiresAt: binding.ExpiresAt, UpdatedAt: now.UTC(),
	}
}

func installReservationFixture(t *testing.T, service *Service, fixture *reservationCoordinatorFixture) resources.ReservationOwner {
	t.Helper()
	service.resourceReservationTTL = 3 * time.Second
	service.resourceRenewTimeout = time.Second
	owner := resources.ReservationOwner{ProcessID: "fixture-process", DaemonID: "fixture-daemon"}
	closeCoordinator, err := installResourceCoordinator(service, fixture, owner, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeCoordinator() })
	return owner
}

func TestHostReservationBindsPrimaryExecutionAndReleases(t *testing.T) {
	service, _ := autoFixture(t)
	fixture := newReservationCoordinatorFixture()
	owner := installReservationFixture(t, service, fixture)
	// Include room for both the prompt and the estimator's 1024-token reserve.
	result, err := service.Run(context.Background(), Request{ModelID: "a", Prompt: "hello", Profile: "code", ContextTokens: 2048})
	if err != nil || result.TaskID == "" || result.Text != "a" {
		t.Fatal(result, err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.requests) != 1 || fixture.releases != 1 || len(fixture.active) != 0 {
		t.Fatal("reservation lifecycle", len(fixture.requests), fixture.releases, len(fixture.active))
	}
	request := fixture.requests[0]
	configID, _ := settingsConfigID(service.settings)
	if request.Owner != owner || request.TaskID != result.TaskID || request.SessionID != result.TaskID ||
		request.ProviderID != "local" || request.ModelID != "a" || request.Profile != "code" ||
		request.RAMBytes != 100 || request.ContextTokens != 2048 || request.ConfigDigest != configID {
		t.Fatal("wrong durable binding", request)
	}
}

func TestHostReservationRejectsDuplicateAndLateInstallation(t *testing.T) {
	service, cfg := autoFixture(t)
	fixture := newReservationCoordinatorFixture()
	installReservationFixture(t, service, fixture)
	owner := resources.ReservationOwner{ProcessID: "other-process", DaemonID: "other-daemon"}
	if _, err := installResourceCoordinator(service, newReservationCoordinatorFixture(), owner, func() error { return nil }); !errors.Is(err, ErrAdmission) {
		t.Fatal("duplicate install accepted", err)
	}
	late, _ := NewService(cfg, nil)
	late.profile = service.profile
	release, err := late.reserveExplicit(context.Background(), cfg.Models[0])
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err = installResourceCoordinator(late, newReservationCoordinatorFixture(), owner, func() error { return nil }); !errors.Is(err, ErrAdmission) {
		t.Fatal("late install accepted", err)
	}
}

func TestHostReservationOneSlotContentionAcrossServices(t *testing.T) {
	first, firstConfig := autoFixture(t)
	second, secondConfig := autoFixture(t)
	budget, err := resources.NewAdaptiveBudget(resources.Limits{MaxConcurrent: 4, RAMPercent: 100, VRAMPercent: 100, MaxAge: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := resources.NewInMemoryCoordinator(budget)
	if err != nil {
		t.Fatal(err)
	}
	firstOwner := resources.ReservationOwner{ProcessID: "fixture-process", DaemonID: "daemon-first"}
	secondOwner := resources.ReservationOwner{ProcessID: "fixture-process", DaemonID: "daemon-second"}
	if _, err = installResourceCoordinator(first, coordinator, firstOwner, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err = installResourceCoordinator(second, coordinator, secondOwner, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	request := func(task string) Request {
		return Request{taskID: task, sessionID: task, Profile: "default"}
	}
	_, releaseFirst, err := first.reservePrimary(context.Background(), context.Background(), firstConfig.Models[0], request("first-task"))
	if err != nil {
		t.Fatal(err)
	}
	if _, release, err := second.reservePrimary(context.Background(), context.Background(), secondConfig.Models[0], request("second-task")); !errors.Is(err, resources.ErrCapacity) || release != nil {
		t.Fatal("second service escaped host slot", err)
	}
	if err = releaseFirst(); err != nil {
		t.Fatal(err)
	}
	_, releaseSecond, err := second.reservePrimary(context.Background(), context.Background(), secondConfig.Models[0], request("second-task"))
	if err != nil {
		t.Fatal("capacity retry retained a durable tombstone", err)
	}
	if err = releaseSecond(); err != nil {
		t.Fatal(err)
	}
}

func TestHostReservationBlockedRenewCannotWedgeRelease(t *testing.T) {
	service, _ := autoFixture(t)
	fixture := newReservationCoordinatorFixture()
	renewEntered := make(chan struct{}, 1)
	fixture.renew = func(ctx context.Context) error {
		select {
		case renewEntered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	}
	installReservationFixture(t, service, fixture)
	providerRelease := make(chan struct{})
	// Hold execution until the bounded renewal call is in flight, then allow
	// provider teardown to reach reservation release while Renew is blocked.
	service.providerFactory = blockingReservationProviderFactory{release: providerRelease}
	done := make(chan error, 1)
	go func() {
		_, err := service.Run(context.Background(), Request{ModelID: "a", Prompt: "hello"})
		done <- err
	}()
	select {
	case <-renewEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("renewal did not start")
	}
	close(providerRelease)
	select {
	case err := <-done:
		if !errors.Is(err, ErrAdmission) {
			t.Fatal("renewal loss not surfaced", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked renewal wedged release")
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.releases != 1 || len(fixture.active) != 0 {
		t.Fatal("lost renewal did not release after provider joined", fixture.releases, len(fixture.active))
	}
}

type blockingReservationProviderFactory struct{ release <-chan struct{} }

func (f blockingReservationProviderFactory) Build(context.Context, providers.Connection) (providers.Provider, error) {
	return blockingReservationProvider{release: f.release}, nil
}

type blockingReservationProvider struct{ release <-chan struct{} }

func (p blockingReservationProvider) Models(context.Context) ([]string, error) {
	return []string{"a", "z"}, nil
}
func (p blockingReservationProvider) Stream(ctx context.Context, _ providers.Request, emit func(providers.Chunk) error) error {
	select {
	case <-p.release:
		return emit(providers.Chunk{Text: "answer", Done: true, FinishReason: "stop"})
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestCoordinatedManagedResidencyNeverUsesPeerUnawareUnload(t *testing.T) {
	service, fixture := managedResidencyFixture(t)
	coordinator := newReservationCoordinatorFixture()
	installReservationFixture(t, service, coordinator)
	service.profile = func(context.Context) (resources.Snapshot, error) {
		return resources.Snapshot{Time: time.Now(), CPUs: 4, TotalRAM: 64 << 30, AvailableRAM: 20 << 30}, nil
	}
	result, err := service.Run(context.Background(), Request{ModelID: "a", Prompt: "hello"})
	if err != nil || result.Text != "answer" {
		t.Fatal(result, err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.unloads != 0 || fixture.streams != 1 {
		t.Fatal("coordinated path used process-local unload authority", fixture.unloads, fixture.streams)
	}
}

func TestHostReservationHealthAndMetricsAreIdentifierFree(t *testing.T) {
	service, cfg := autoFixture(t)
	coordinator := newReservationCoordinatorFixture()
	installReservationFixture(t, service, coordinator)
	db, err := telemetry.Open(context.Background(), service.settings.Telemetry.Database)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	_, release, err := service.reservePrimary(context.Background(), context.Background(), cfg.Models[0], Request{taskID: "metrics-task", sessionID: "metrics-session", Profile: "default"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()

	snapshot, err := service.Metrics(context.Background())
	if err != nil || snapshot.Validate() != nil || snapshot.Resources == nil {
		t.Fatal(snapshot, err)
	}
	want := map[string]int64{"reservation_coordinator": 1, "reservations_active": 1, "reserved_ram_bytes": 100}
	for _, measurement := range snapshot.Resources.Measurements {
		if value, ok := want[measurement.Name]; ok {
			if !measurement.Available || measurement.Value != value {
				t.Fatal(measurement)
			}
			delete(want, measurement.Name)
		}
	}
	if len(want) != 0 {
		t.Fatal("missing reservation metrics", want)
	}
	report, err := service.HealthReport(context.Background(), healthySupervisor())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, check := range report.Checks {
		if check.Component == "resources" && check.ID == "reservations" {
			found = check.Status == "healthy" && check.Code == "available"
		}
	}
	if !found {
		t.Fatal("missing reservation coordinator health", report)
	}
}
