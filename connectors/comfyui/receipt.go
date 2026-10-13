package comfyui

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Receipt is a private write-ahead submission record, not server idempotency.
// A prepared/uncertain record blocks replay even after a process crash.
type Receipt struct {
	Version        int    `json:"version"`
	Endpoint       string `json:"endpoint"`
	ClientID       string `json:"client_id"`
	WorkflowSHA256 string `json:"workflow_sha256"`
	State          string `json:"state"`
	PromptID       string `json:"prompt_id,omitempty"`
}

func LoadReceipt(file string) (Receipt, error) {
	var r Receipt
	info, e := os.Lstat(file)
	if e != nil {
		return r, e
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return r, errors.New("receipt must be a private regular file")
	}
	f, e := os.Open(file)
	if e != nil {
		return r, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, MaxJSON+1))
	if e != nil {
		return r, e
	}
	if len(b) > MaxJSON {
		return r, errors.New("receipt exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&r) != nil || decoder.Decode(new(any)) != io.EOF || r.Version != 1 || !validID(r.ClientID) {
		return r, errors.New("invalid receipt")
	}
	if _, e := New(r.Endpoint); e != nil {
		return Receipt{}, errors.New("invalid receipt origin")
	}
	digest, e := hex.DecodeString(r.WorkflowSHA256)
	if e != nil || len(digest) != sha256.Size {
		return Receipt{}, errors.New("invalid receipt digest")
	}
	switch r.State {
	case "accepted":
		if !validID(r.PromptID) {
			return Receipt{}, errors.New("invalid accepted receipt")
		}
	case "prepared", "uncertain", "rejected":
		if r.PromptID != "" {
			return Receipt{}, errors.New("invalid receipt identity")
		}
	default:
		return Receipt{}, errors.New("invalid receipt state")
	}
	return r, nil
}
func (c *Client) SubmitOnce(ctx context.Context, w Workflow, file string) (r Receipt, err error) {
	b, e := json.Marshal(w)
	if e != nil {
		return r, e
	}
	if _, e = ParseWorkflow(b); e != nil {
		return r, e
	}
	digest := sha256.Sum256(b)
	r = Receipt{Version: 1, Endpoint: c.Endpoint(), ClientID: rand.Text(), WorkflowSHA256: hex.EncodeToString(digest[:]), State: "prepared"}
	// Exclusive creation is also the cross-process dispatch lock. Never remove it
	// on errors: delivery cannot be inferred from a local connection failure.
	f, e := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return Receipt{}, errors.New("receipt exists or cannot be created; inspect it instead of resubmitting")
	}
	e = json.NewEncoder(f).Encode(r)
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return r, e
	}
	if closeErr != nil {
		return r, closeErr
	}
	dir, e := os.Open(directory(file))
	if e != nil {
		return r, e
	}
	e = dir.Sync()
	dir.Close()
	if e != nil {
		return r, e
	}
	s, e := c.Submit(ctx, w, r.ClientID)
	if e != nil {
		r.State = "uncertain"
		if !errors.Is(e, ErrUncertain) {
			r.State = "rejected"
		}
	} else {
		r.State = "accepted"
		r.PromptID = s.PromptID
	}
	// Atomic replacement keeps the pre-send record intact if persistence fails.
	if saveErr := saveReceipt(file, r); saveErr != nil {
		return r, errors.New("submission receipt update failed; do not resubmit")
	}
	return r, e
}

func directory(file string) string { return filepath.Dir(file) }
func saveReceipt(file string, r Receipt) error {
	f, e := os.CreateTemp(directory(file), ".comfy-receipt-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	e = json.NewEncoder(f).Encode(r)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = os.Rename(name, file); e != nil {
		return e
	}
	d, e := os.Open(directory(file))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
