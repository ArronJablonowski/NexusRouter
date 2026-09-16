package resources

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func validReservationRequest(now time.Time) ReservationRequest {
	return ReservationRequest{
		Version: ReservationContractVersion, ReservationID: "reservation-1", HostScope: "host-scope-1",
		Owner: ReservationOwner{ProcessID: "process-1", DaemonID: "daemon-1"}, TaskID: "task-1", SessionID: "session-1",
		ProviderID: "provider-1", ModelID: "model:latest", Profile: "code", GPUDevice: "nvidia:GPU-ABCDEF01",
		RAMBytes: 2 << 30, VRAMBytes: 3 << 30, ContextTokens: 8192, ConfigDigest: strings.Repeat("a", 64),
		RequestedAt: now, TTL: 30 * time.Second,
	}
}

func TestReservationRequestValidationAndSafeShape(t *testing.T) {
	now := time.Unix(100, 200).UTC()
	valid := validReservationRequest(now)
	if valid.Validate() != nil {
		t.Fatal("valid request rejected")
	}
	mutations := map[string]func(*ReservationRequest){
		"version":          func(r *ReservationRequest) { r.Version = 2 },
		"reservation":      func(r *ReservationRequest) { r.ReservationID = " bad" },
		"host":             func(r *ReservationRequest) { r.HostScope = "" },
		"process":          func(r *ReservationRequest) { r.Owner.ProcessID = "" },
		"daemon":           func(r *ReservationRequest) { r.Owner.DaemonID = "private\nvalue" },
		"task":             func(r *ReservationRequest) { r.TaskID = "" },
		"session":          func(r *ReservationRequest) { r.SessionID = "" },
		"provider":         func(r *ReservationRequest) { r.ProviderID = "" },
		"model":            func(r *ReservationRequest) { r.ModelID = "" },
		"profile":          func(r *ReservationRequest) { r.Profile = "" },
		"device":           func(r *ReservationRequest) { r.GPUDevice = "nvidia:../../private" },
		"ram":              func(r *ReservationRequest) { r.RAMBytes = 0 },
		"device_vram":      func(r *ReservationRequest) { r.VRAMBytes = 0 },
		"context_zero":     func(r *ReservationRequest) { r.ContextTokens = 0 },
		"context_large":    func(r *ReservationRequest) { r.ContextTokens = 1<<30 + 1 },
		"config":           func(r *ReservationRequest) { r.ConfigDigest = strings.Repeat("A", 64) },
		"requested":        func(r *ReservationRequest) { r.RequestedAt = time.Time{} },
		"ttl_short":        func(r *ReservationRequest) { r.TTL = time.Second - time.Millisecond },
		"ttl_long":         func(r *ReservationRequest) { r.TTL = 10*time.Minute + time.Millisecond },
		"ttl_noncanonical": func(r *ReservationRequest) { r.TTL = time.Second + time.Nanosecond },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if candidate.Validate() == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
	body, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"prompt", "output", "credential", "endpoint", "api_key"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatal("unsafe request field", string(body))
		}
	}
}

func TestReservationCanonicalDigestAndBinding(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 200, time.FixedZone("equivalent", -7*60*60))
	first := validReservationRequest(now)
	second := first
	second.RequestedAt = now.UTC()
	second.GPUDevice = "nvidia:GPU-abcdef01"
	a, err := first.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.CanonicalDigest()
	if err != nil || a != b || !reservationDigest(a) {
		t.Fatal("digest is not canonical", a, b, err)
	}
	second.RequestedAt = second.RequestedAt.Add(4 * time.Second)
	retried, err := second.CanonicalDigest()
	if err != nil || retried != a {
		t.Fatal("retry observation time changed capacity identity", a, retried, err)
	}
	canonical, err := first.Canonical()
	if err != nil || canonical.GPUDevice != "nvidia:GPU-abcdef01" || canonical.RequestedAt.Location() != time.UTC {
		t.Fatal("request was not canonicalized", canonical, err)
	}
	second.ModelID = "different"
	c, _ := second.CanonicalDigest()
	if c == a {
		t.Fatal("material binding omitted from digest")
	}
	normalized := normalizeReservationRequest(first)
	binding := ReservationBinding{Version: 1, Request: normalized, RequestDigest: a, AcquiredAt: now.UTC(), ExpiresAt: now.UTC().Add(first.TTL)}
	if binding.Validate() != nil {
		t.Fatal("valid binding rejected")
	}
	binding.ExpiresAt = binding.ExpiresAt.Add(time.Second)
	if binding.Validate() != nil {
		t.Fatal("renewed binding rejected")
	}
	binding.RequestDigest = strings.Repeat("0", 64)
	if binding.Validate() == nil {
		t.Fatal("forged binding accepted")
	}
}

func TestReservationPublicTypesDoNotGrowPayloadFields(t *testing.T) {
	requestFields := []string{"Version", "ReservationID", "HostScope", "Owner", "TaskID", "SessionID", "ProviderID", "ModelID", "Profile", "GPUDevice", "RAMBytes", "VRAMBytes", "ContextTokens", "ConfigDigest", "RequestedAt", "TTL"}
	typeOf := reflect.TypeOf(ReservationRequest{})
	if typeOf.NumField() != len(requestFields) {
		t.Fatal("review new reservation fields for sensitive payloads", typeOf.NumField())
	}
	for index, name := range requestFields {
		if typeOf.Field(index).Name != name {
			t.Fatal("unexpected reservation shape", index, typeOf.Field(index).Name)
		}
	}
	snapshot := reflect.TypeOf(ReservationSnapshot{})
	for index := 0; index < snapshot.NumField(); index++ {
		name := strings.ToLower(snapshot.Field(index).Name)
		for _, forbidden := range []string{"owner", "host", "task", "session", "provider", "model", "profile", "deviceid", "digest"} {
			if strings.Contains(name, forbidden) {
				t.Fatal("aggregate snapshot exposes identifier", snapshot.Field(index).Name)
			}
		}
	}
}

func TestReservationErrorsAreDistinct(t *testing.T) {
	for _, pair := range [][2]error{{ErrReservation, ErrReservationConflict}, {ErrReservationConflict, ErrReservationOwner}, {ErrReservationOwner, ErrReservationExpired}} {
		if errors.Is(pair[0], pair[1]) || errors.Is(pair[1], pair[0]) {
			t.Fatal("reservation errors collapsed", pair)
		}
	}
}
