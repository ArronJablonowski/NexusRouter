package telegrambridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ArronJablonowski/NexusRouter/connectors/telegram"
	"github.com/ArronJablonowski/NexusRouter/health"
	"github.com/ArronJablonowski/NexusRouter/internal/app"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

var ErrConfiguration = errors.New("Telegram connector configuration unavailable")

type Worker struct {
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
	state  atomic.Int32
}

// Start is inert when disabled: no secrets are read, no directory is created,
// and no Telegram API request is made. Call only after daemon initialization.
func Start(ctx context.Context, s config.Settings, secret func(string) string, daemonID string, report func(string)) (*Worker, error) {
	if !s.Telegram.Enabled {
		return nil, nil
	}
	if ctx == nil || s.Validate() != nil || secret == nil || report == nil {
		return nil, ErrConfiguration
	}
	token := secret(s.Telegram.TokenEnv)
	bot, err := telegram.BotID(token)
	if err != nil {
		return nil, ErrConfiguration
	}
	client, err := telegram.NewClient(token)
	if err != nil {
		return nil, ErrConfiguration
	}
	settings := chatSettings(s)
	body, err := json.Marshal(settings)
	if err != nil {
		return nil, ErrConfiguration
	}
	digest := sha256.Sum256(body)
	store, err := openStore(s.Telegram.StateDirectory, identity(bot, hex.EncodeToString(digest[:])))
	if err != nil {
		return nil, err
	}
	fail := func(e error) (*Worker, error) { store.close(); return nil, e }
	service, err := app.NewService(settings, secret)
	if err != nil {
		return fail(ErrConfiguration)
	}
	closeResources, err := app.InstallHostResourceCoordinator(ctx, service, daemonID)
	if err != nil {
		return fail(ErrConfiguration)
	}
	runCtx, cancel := context.WithCancel(ctx)
	dispatcher, err := app.StartDispatcher(runCtx, service)
	if err != nil {
		cancel()
		closeResources()
		return fail(ErrConfiguration)
	}
	b := &bridge{store: store, api: client, backend: serviceBackend{service: service, model: s.Telegram.ModelID, policyDB: s.Telemetry.Database, token: token}, allowed: map[int64]bool{}}
	for _, id := range s.Telegram.AllowedUsers {
		b.allowed[id] = true
	}
	worker := &Worker{cancel: cancel, done: make(chan struct{})}
	b.observe = func(err error) {
		if err == nil {
			worker.state.Store(1)
		} else {
			worker.state.Store(2)
		}
	}
	go func() {
		defer close(worker.done)
		defer worker.state.Store(3)
		defer cancel()
		defer client.Close()
		defer store.close()
		defer closeResources()
		defer dispatcher.Close()
		last := ""
		b.run(runCtx, func(message string) {
			if message != last {
				report(message)
				last = message
			}
		})
	}()
	return worker, nil
}
func (w *Worker) Close() {
	if w == nil {
		return
	}
	w.once.Do(func() { w.cancel(); <-w.done })
}

// Use a dedicated dispatcher/database so Telegram can never continue another
// client's task. Host-wide resource reservations still coordinate all workers.
func chatSettings(s config.Settings) config.Settings {
	d := config.Defaults()
	s.Telemetry.Database = filepath.Join(s.Telegram.StateDirectory, "tasks.db")
	s.Security.RedactEnv = append([]string(nil), s.Security.RedactEnv...)
	if !slices.Contains(s.Security.RedactEnv, s.Telegram.TokenEnv) {
		s.Security.RedactEnv = append(s.Security.RedactEnv, s.Telegram.TokenEnv)
	}
	s.Telegram = config.Telegram{}
	s.Tools = config.Tools{MaxTurns: 2}
	s.ComfyUI = config.ComfyUI{}
	s.NativeHarnesses = nil
	s.NativeHarnessEvidenceDir = ""
	s.Skills = d.Skills
	s.Skills.Enabled = false
	s.Skills.AutoDraft = false
	s.Skills.AutoActivate = false
	s.Skills.Rollback = false
	s.Memory = d.Memory
	s.Memory.Enabled = false
	s.Workers.DelegateReadTools = false
	s.Workers.DelegateModel = ""
	s.Routing.Classifier.Enabled = false
	s.Evaluation.AutoReviewModel = ""
	s.Evaluation.Judge = false
	return s
}

type serviceBackend struct {
	service                *app.Service
	model, policyDB, token string
}

func (b serviceBackend) Submit(ctx context.Context, key, text, previous string) (submissions.Status, error) {
	if strings.Contains(text, b.token) && b.token != "" {
		return submissions.Status{}, ErrState
	}
	if !config.ModelUseAllowed(b.policyDB, "local", b.model) {
		return submissions.Status{}, ErrState
	}
	return b.service.Submit(ctx, key, app.Request{ModelID: b.model, Prompt: text, ContinueTaskID: previous, LocalRequired: true, DisableFallback: true, Domain: "chat", Profile: "default"})
}
func (b serviceBackend) Status(ctx context.Context, id string) (submissions.Status, error) {
	return b.service.SubmissionStatus(ctx, id)
}

// Health reports connector observation only, never private chat or bot identity.
func (w *Worker) Health() health.Check {
	c := health.Check{Component: "telegram", Status: "disabled", Code: "disabled_by_policy"}
	if w == nil {
		return c
	}
	switch w.state.Load() {
	case 0:
		c.Status, c.Code = "unknown", "supervisor_starting"
	case 1:
		c.Status, c.Code = "healthy", "available"
	case 3:
		c.Status, c.Code = "unavailable", "supervisor_stopped"
	default:
		c.Status, c.Code = "degraded", "supervisor_error"
	}
	return c
}
