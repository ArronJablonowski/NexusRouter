package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ArronJablonowski/DarwinRouter/health"
	"github.com/ArronJablonowski/DarwinRouter/internal/config"
	"github.com/ArronJablonowski/DarwinRouter/skills"
)

type Learner struct {
	mu               sync.Mutex
	cancel           context.CancelFunc
	done             chan struct{}
	err              error
	disabled, closed bool
	code, status     string
	stepStarted      time.Time
}

func StartLearning(ctx context.Context, s *Service) (*Learner, error) {
	if ctx == nil || ctx.Err() != nil || s == nil {
		return nil, ErrLearningAttention
	}
	l := &Learner{done: make(chan struct{}), status: "unknown", code: "supervisor_starting"}
	if !s.settings.Skills.Learning.Enabled {
		l.disabled = true
		close(l.done)
		return l, nil
	}
	if s.settings.Validate() != nil || !s.settings.Skills.GenerationBudget.Enabled {
		return nil, ErrLearningAttention
	}
	interval, err := config.Duration(s.settings.Skills.Learning.Interval)
	if err != nil || interval < time.Second || interval > 24*time.Hour {
		return nil, ErrLearningAttention
	}
	ctx, l.cancel = context.WithCancel(ctx)
	go func() {
		defer close(l.done)
		defer func() {
			if ctx.Err() != nil {
				l.mu.Lock()
				l.status, l.code = "unavailable", "supervisor_stopped"
				l.mu.Unlock()
			}
		}()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			l.mu.Lock()
			l.stepStarted = time.Now()
			l.mu.Unlock()
			_, err := s.LearningStep(ctx)
			if ctx.Err() != nil {
				return
			}
			l.mu.Lock()
			l.stepStarted = time.Time{}
			if errors.Is(err, skills.ErrGenerationBudget) {
				l.status, l.code = "healthy", "learning_budget_wait"
			} else if err != nil {
				l.status, l.code = "degraded", "supervisor_error"
			} else {
				l.status, l.code = "healthy", "supervisor_ok"
			}
			if err != nil && !errors.Is(err, skills.ErrGenerationBudget) {
				l.err = ErrLearningAttention
				l.mu.Unlock()
				return
			}
			l.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return l, nil
}

func (l *Learner) Close() error {
	if l == nil {
		return nil
	}
	if l.cancel != nil {
		l.cancel()
	}
	<-l.done
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	return l.err
}
func (l *Learner) Health() health.Check {
	out := health.Check{Component: "learning", Status: "unknown", Code: "supervisor_starting"}
	if l == nil {
		return out
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.disabled {
		out.Status, out.Code = "disabled", "disabled_by_policy"
	} else if l.closed {
		out.Status, out.Code = "unavailable", "supervisor_stopped"
	} else {
		out.Status, out.Code = l.status, l.code
		if !l.stepStarted.IsZero() && time.Since(l.stepStarted) > 45*time.Second {
			out.Status, out.Code = "degraded", "supervisor_stalled"
		}
	}
	return out
}
