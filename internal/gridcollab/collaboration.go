// Package gridcollab wires bounded commander consultation to recorded dispatch.
package gridcollab

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/remote"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

type collaborationClient interface {
	Info(context.Context, string) (remote.Info, error)
	DispatchRecorded(context.Context, *remote.RouteStore, string, string, remote.Task) (submissions.Status, error)
	Status(context.Context, string, string) (submissions.Status, error)
	Cancel(context.Context, string, string) (submissions.Status, error)
}
type trustReader interface {
	Read() (remote.Registry, error)
}
type Bridge struct {
	Trust  trustReader
	Client collaborationClient
	Store  *remote.RouteStore
}

func Install(s *app.Service, cfg config.Settings) error {
	c := cfg.WebUI.RemoteClient
	if c == nil || cfg.WebUI.RemoteTrustFile == "" || cfg.WebUI.RemoteDispatchDirectory == "" {
		return nil
	}
	store, err := remote.OpenRouteStore(cfg.WebUI.RemoteDispatchDirectory)
	if err != nil {
		return err
	}
	s.ConfigureCommanderCollaboration(&Bridge{Trust: remote.TrustFile(cfg.WebUI.RemoteTrustFile), Client: &remote.Client{Trust: remote.TrustFile(cfg.WebUI.RemoteTrustFile), UsageFile: cfg.WebUI.RemoteTrustFile + ".usage.db", Credentials: remote.Credentials{CertificateFile: c.CertificateFile, KeyFile: c.KeyFile, CAFile: c.CAFile}}, Store: store})
	return nil
}
func (b *Bridge) eligible(ctx context.Context, id string, tokens int, private bool) (app.CommanderPeer, []string, error) {
	reg, err := b.Trust.Read()
	if err != nil {
		return app.CommanderPeer{}, nil, err
	}
	var peer *remote.Peer
	for i := range reg.Peers {
		if reg.Peers[i].ID == id {
			peer = &reg.Peers[i]
			break
		}
	}
	if peer == nil || (private && !peer.AllowPrivate) || tokens < 1 || tokens > peer.MaxContextTokens {
		return app.CommanderPeer{}, nil, remote.ErrDenied
	}
	for _, op := range []string{"info", "dispatch", "inspect", "cancel"} {
		if !slices.Contains(peer.Operations, op) {
			return app.CommanderPeer{}, nil, remote.ErrDenied
		}
	}
	info, err := b.Client.Info(ctx, id)
	if err != nil {
		return app.CommanderPeer{}, nil, err
	}
	if !info.Available || info.HybridVersion < 2 || info.Routing == nil || info.Routing.CommanderID == "" {
		return app.CommanderPeer{}, nil, remote.ErrUnavailable
	}
	commander := info.Routing.CommanderID
	found := false
	var workers []string
	for _, m := range info.Models {
		if !slices.Contains(peer.Models, m.ID) || (!m.Local && (private || !peer.AllowCloudInference)) || m.EstimatedCost == nil || *m.EstimatedCost != 0 || m.ContextTokens < tokens {
			continue
		}
		if m.ID == commander {
			found = true
		} else if len(workers) < 1 {
			workers = append(workers, m.ID)
		}
	}
	if !found {
		return app.CommanderPeer{}, nil, remote.ErrUnavailable
	}
	return app.CommanderPeer{InstanceID: id, ModelID: commander, Hostname: info.Hostname}, workers, nil
}
func (b *Bridge) List(ctx context.Context, tokens int, private bool) ([]app.CommanderPeer, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	reg, err := b.Trust.Read()
	if err != nil {
		return nil, err
	}
	out := []app.CommanderPeer{}
	for _, p := range reg.Peers {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		peer, _, err := b.eligible(ctx, p.ID, tokens, private)
		if err == nil {
			out = append(out, peer)
		}
	}
	return out, nil
}
func (b *Bridge) Consult(ctx context.Context, instance, key, prompt, validation string, tokens int, private bool) (app.Result, error) {
	peer, _, err := b.eligible(ctx, instance, tokens, private)
	if err != nil {
		return app.Result{}, err
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > time.Hour {
		return app.Result{}, remote.ErrInvalid
	}
	task := remote.Task{Version: 1, ModelID: peer.ModelID, Prompt: prompt, Domain: "general", Profile: "default", ContextTokens: tokens, MaxCost: 0, Private: private, Execution: &runtime.RemoteExecution{Mode: "consult", Depth: 1, Deadline: deadline}}
	status, err := b.Client.DispatchRecorded(ctx, b.Store, instance, key, task)
	// Never resend uncertain dispatch. Preserve the immutable route for inspection.
	if err != nil {
		return app.Result{}, err
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		switch status.State {
		case "completed":
			if status.Result == nil {
				return app.Result{}, remote.ErrUnavailable
			}
			return app.Result{TaskID: status.Result.TaskID, Text: status.Result.Text}, nil
		case "failed", "canceled":
			return app.Result{}, errors.New("remote commander did not complete")
		}
		select {
		case <-ctx.Done():
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			_, _ = b.Client.Cancel(cleanup, instance, key)
			cancel()
			return app.Result{}, ctx.Err()
		case <-ticker.C:
		}
		status, err = b.Client.Status(ctx, instance, key)
		if err != nil {
			return app.Result{}, err
		}
	}
}
