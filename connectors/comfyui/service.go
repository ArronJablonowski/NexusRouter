package comfyui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
)

var ErrJob = errors.New("ComfyUI job unavailable or conflicts with recorded workflow")

// Service pins both the endpoint and private receipt directory. User/model
// arguments contain opaque job keys, never endpoint URLs or filesystem paths.
type Service struct {
	Client    *Client
	directory string
}
type Job struct {
	Replayed bool     `json:"replayed,omitempty"`
	Key      string   `json:"key"`
	State    string   `json:"state"`
	PromptID string   `json:"prompt_id,omitempty"`
	History  *History `json:"history,omitempty"`
}

func OpenService(endpoint, directory string) (*Service, error) {
	c, e := New(endpoint)
	if e != nil {
		return nil, e
	}
	if !filepath.IsAbs(directory) {
		return nil, ErrJob
	}
	info, e := os.Lstat(directory)
	if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("ComfyUI receipts require an existing private directory (0700)")
	}
	return &Service{c, directory}, nil
}
func (s *Service) file(key string) (string, error) {
	if !validID(key) {
		return "", ErrJob
	}
	return filepath.Join(s.directory, key+".json"), nil
}
func (s *Service) receipt(key string) (Receipt, error) {
	var r Receipt
	f, e := s.file(key)
	if e != nil {
		return r, e
	}
	i, e := os.Lstat(f)
	if e != nil || !i.Mode().IsRegular() || i.Mode().Perm()&0077 != 0 {
		return r, ErrJob
	}
	r, e = LoadReceipt(f)
	if e != nil || r.Endpoint != s.Client.Endpoint() {
		return r, ErrJob
	}
	return r, nil
}
func (s *Service) Submit(ctx context.Context, key string, w Workflow) (Job, error) {
	f, e := s.file(key)
	if e != nil {
		return Job{}, e
	}
	if _, e = os.Lstat(f); e == nil {
		r, e := s.receipt(key)
		b, _ := json.Marshal(w)
		d := sha256.Sum256(b)
		if e != nil || r.WorkflowSHA256 != hex.EncodeToString(d[:]) {
			return Job{}, ErrJob
		}
		return Job{Key: key, State: r.State, PromptID: r.PromptID, Replayed: true}, nil
	} else if !os.IsNotExist(e) {
		return Job{}, ErrJob
	}
	r, e := s.Client.SubmitOnce(ctx, w, f)
	return Job{Key: key, State: r.State, PromptID: r.PromptID}, e
}
func (s *Service) Status(ctx context.Context, key string) (Job, error) {
	r, e := s.receipt(key)
	if e != nil {
		return Job{}, e
	}
	j := Job{Key: key, State: r.State, PromptID: r.PromptID}
	if r.PromptID != "" {
		j.History, e = s.Client.History(ctx, r.PromptID)
		if j.History != nil {
			if j.History.Status.Completed || j.History.Status.StatusStr == "error" {
				j.State = j.History.Status.StatusStr
			}
		}
	}
	return j, e
}
func (s *Service) List() ([]Job, error) {
	entries, e := os.ReadDir(s.directory)
	if e != nil || len(entries) > 1000 {
		return nil, ErrJob
	}
	jobs := []Job{}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		key := entry.Name()[:len(entry.Name())-5]
		r, e := s.receipt(key)
		if e == nil {
			jobs = append(jobs, Job{Key: key, State: r.State, PromptID: r.PromptID})
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Key < jobs[j].Key })
	return jobs, nil
}
func (s *Service) CancelPending(ctx context.Context, key string) error {
	r, e := s.receipt(key)
	if e != nil || r.State != "accepted" || r.PromptID == "" {
		return ErrJob
	}
	return s.Client.DeletePending(ctx, r.PromptID)
}
func (s *Service) Output(ctx context.Context, key, node string, index int) ([]byte, error) {
	j, e := s.Status(ctx, key)
	if e != nil || j.History == nil || j.State != "success" || index < 0 {
		return nil, ErrJob
	}
	images := j.History.Outputs[node].Images
	if index >= len(images) {
		return nil, ErrJob
	}
	return s.Client.Image(ctx, images[index])
}

// ValidJobKey validates an opaque identifier, never a path or endpoint.
func ValidJobKey(key string) bool { return validID(key) }
