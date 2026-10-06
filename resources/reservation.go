package resources

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const ReservationContractVersion = 1

const (
	minReservationTTL = time.Second
	maxReservationTTL = 10 * time.Minute
)

var (
	ErrReservation         = errors.New("resource reservation unavailable")
	ErrReservationConflict = errors.New("resource reservation identity conflict")
	ErrReservationOwner    = errors.New("resource reservation owner mismatch")
	ErrReservationExpired  = errors.New("resource reservation expired")
)

// ReservationOwner is exact, process-local authority. These opaque identifiers
// are durable bindings, not operating-system PIDs or public diagnostics.
type ReservationOwner struct {
	ProcessID string `json:"process_id"`
	DaemonID  string `json:"daemon_id"`
}

func (o ReservationOwner) Validate() error {
	if !reservationText(o.ProcessID, 128) || !reservationText(o.DaemonID, 128) {
		return ErrReservation
	}
	return nil
}

// ReservationRequest binds a local capacity claim to its exact execution.
// It intentionally has no prompt, output, endpoint, credential or free-form
// metadata fields. RAM includes weights and context/KV memory.
type ReservationRequest struct {
	BackendManagedRAM bool `json:"backend_managed_ram,omitempty"`
	// Nonzero ColdRAMBytes marks a qualified warm estimate. This preserves the
	// original full estimate and pinned model identity in the durable receipt;
	// RAMBytes remains the actual incremental charge, never negative credit.
	ColdRAMBytes    uint64           `json:"cold_ram_bytes,omitempty"`
	ResidencyDigest string           `json:"residency_digest,omitempty"`
	Version         int              `json:"version"`
	ReservationID   string           `json:"reservation_id"`
	HostScope       string           `json:"host_scope"`
	Owner           ReservationOwner `json:"owner"`
	TaskID          string           `json:"task_id"`
	SessionID       string           `json:"session_id"`
	ProviderID      string           `json:"provider_id"`
	ModelID         string           `json:"model_id"`
	Profile         string           `json:"profile"`
	GPUDevice       string           `json:"gpu_device,omitempty"`
	RAMBytes        uint64           `json:"ram_bytes"`
	VRAMBytes       uint64           `json:"vram_bytes"`
	ContextTokens   int              `json:"context_tokens"`
	ConfigDigest    string           `json:"config_digest"`
	RequestedAt     time.Time        `json:"requested_at"`
	TTL             time.Duration    `json:"ttl"`
}

func (r ReservationRequest) Validate() error {
	if r.BackendManagedRAM && (r.VRAMBytes != 0 || r.GPUDevice != "" || r.ColdRAMBytes != 0 || r.ResidencyDigest != "") {
		return ErrReservation
	}
	if r.ColdRAMBytes != 0 || r.ResidencyDigest != "" {
		if r.ColdRAMBytes <= r.RAMBytes || !reservationDigest(r.ResidencyDigest) || r.VRAMBytes != 0 || r.GPUDevice != "" {
			return ErrReservation
		}
	}
	if r.Version != ReservationContractVersion || !reservationText(r.ReservationID, 128) ||
		!reservationText(r.HostScope, 128) || r.Owner.Validate() != nil ||
		!reservationText(r.TaskID, 128) || !reservationText(r.SessionID, 128) ||
		!reservationText(r.ProviderID, 128) || !reservationText(r.ModelID, 256) ||
		!reservationText(r.Profile, 128) || len(r.GPUDevice) > 128 ||
		(r.GPUDevice != "" && !ValidGPUDeviceID(r.GPUDevice)) ||
		ValidateNeed(Need{RAM: r.RAMBytes, VRAM: r.VRAMBytes, Device: r.GPUDevice}) != nil ||
		r.ContextTokens < 1 || r.ContextTokens > 1<<30 || !reservationDigest(r.ConfigDigest) ||
		!reservationTime(r.RequestedAt) || r.TTL < minReservationTTL || r.TTL > maxReservationTTL ||
		r.TTL%time.Millisecond != 0 {
		return ErrReservation
	}
	return nil
}

func normalizeReservationRequest(r ReservationRequest) ReservationRequest {
	r.RequestedAt = r.RequestedAt.UTC()
	r.GPUDevice = canonicalGPUDevice(r.GPUDevice)
	return r
}

// Canonical returns the owned representation used by durable coordinators.
func (r ReservationRequest) Canonical() (ReservationRequest, error) {
	if r.Validate() != nil {
		return ReservationRequest{}, ErrReservation
	}
	return normalizeReservationRequest(r), nil
}

func canonicalGPUDevice(device string) string {
	if strings.HasPrefix(strings.ToLower(device), "nvidia:gpu-") {
		return "nvidia:GPU-" + strings.ToLower(device[len("nvidia:GPU-"):])
	}
	return strings.ToLower(device)
}

// CanonicalDigest is the idempotency identity for an exact capacity binding.
// RequestedAt is freshness evidence, not execution identity, so a caller can
// safely retry an acknowledgement loss with a new observation time. Equivalent
// GPU aliases likewise produce one digest.
func (r ReservationRequest) CanonicalDigest() (string, error) {
	var err error
	r, err = r.Canonical()
	if err != nil {
		return "", err
	}
	r.RequestedAt = time.Unix(0, 0).UTC()
	body, err := json.Marshal(r)
	if err != nil || len(body) > 4096 {
		return "", ErrReservation
	}
	sum := sha256.Sum256(append([]byte("darwin.resources.reservation.request.v1\x00"), body...))
	return hex.EncodeToString(sum[:]), nil
}

// ReservationBinding is the immutable result of successful admission.
type ReservationBinding struct {
	Version       int                `json:"version"`
	Request       ReservationRequest `json:"request"`
	RequestDigest string             `json:"request_digest"`
	AcquiredAt    time.Time          `json:"acquired_at"`
	ExpiresAt     time.Time          `json:"expires_at"`
}

func (b ReservationBinding) Validate() error {
	digest, err := b.Request.CanonicalDigest()
	if b.Version != ReservationContractVersion || err != nil || digest != b.RequestDigest ||
		!reservationTime(b.AcquiredAt) || !reservationTime(b.ExpiresAt) ||
		b.AcquiredAt.Location() != time.UTC || b.ExpiresAt.Location() != time.UTC ||
		b.ExpiresAt.Before(b.AcquiredAt.Add(b.Request.TTL)) {
		return ErrReservation
	}
	return nil
}

func reservationText(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if char < 0x21 || char > 0x7e {
			return false
		}
	}
	return true
}

func reservationDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func reservationTime(value time.Time) bool {
	year := value.UTC().Year()
	return !value.IsZero() && year >= 1970 && year < 2261
}
