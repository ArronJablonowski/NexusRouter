package app

import (
	"context"
	"errors"
	"time"

	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/runtime"
	"github.com/ArronJablonowski/NexusRouter/submissions"
)

var errRecoverySecretResolution = errors.New("recovery secret resolution failed")

func (d *Dispatcher) recoverInterruptedModel(ctx context.Context, item submissions.Summary, configDigest string) (telemetry.SubmissionRecoveryCommit, error) {
	return d.recoverInterrupted(ctx, item, configDigest, false)
}

func (d *Dispatcher) recoverInterruptedDelegation(ctx context.Context, item submissions.Summary, configDigest string) (telemetry.SubmissionRecoveryCommit, error) {
	return d.recoverInterrupted(ctx, item, configDigest, true)
}

func (d *Dispatcher) recoverInterrupted(ctx context.Context, item submissions.Summary, configDigest string, delegation bool) (telemetry.SubmissionRecoveryCommit, error) {
	secrets, secretErr := resolveRecoverySecrets(d.recoverySecrets)
	if secretErr != nil {
		return telemetry.SubmissionRecoveryCommit{}, secretErr
	}
	release := func() {}
	if d.eventSinkSequencer != nil {
		release = d.eventSinkSequencer.acquire(item.TaskIDs)
	}
	defer release()
	var commit telemetry.SubmissionRecoveryCommit
	var err error
	if delegation {
		commit, err = d.db.RecoverInterruptedDelegationCommitScreened(ctx, item.ID, configDigest, time.Now().UTC(), secrets)
	} else {
		commit, err = d.db.RecoverInterruptedModelCommitScreened(ctx, item.ID, configDigest, time.Now().UTC(), secrets)
	}
	if err != nil || !commit.Changed {
		return commit, err
	}
	deliveryCtx := d.lifecycle
	if deliveryCtx == nil {
		deliveryCtx = ctx
	}
	deliveryCtx, cancel := context.WithTimeout(deliveryCtx, 5*time.Second)
	defer cancel()
	if d.deliverRecoveryEvents(deliveryCtx, commit.Events) != nil {
		d.recordError()
	}
	return commit, nil
}

func resolveRecoverySecrets(resolve func() []string) (secrets []string, err error) {
	if resolve == nil {
		return nil, nil
	}
	defer func() {
		if recover() != nil {
			secrets, err = nil, errRecoverySecretResolution
		}
	}()
	return append([]string(nil), resolve()...), nil
}

// deliverRecoveryEvents observes only events committed by the immediately
// preceding recovery call. Delivery is live-only: failures never roll back or
// authorize replay, and explicit journal reads remain the catch-up mechanism.
func (d *Dispatcher) deliverRecoveryEvents(ctx context.Context, events []runtime.Event) error {
	if d.eventSink == nil {
		return nil
	}
	for _, event := range events {
		clone, err := event.Clone()
		if err != nil || invokeRecoveryEventSink(ctx, d.eventSink, clone) != nil {
			return ErrEventDelivery
		}
	}
	return nil
}

func invokeRecoveryEventSink(ctx context.Context, sink runtime.EventSink, event runtime.Event) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrEventDelivery
		}
	}()
	if sink.Emit(ctx, event) != nil {
		return ErrEventDelivery
	}
	return nil
}
