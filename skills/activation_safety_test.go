package skills

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestActivationSafetyAdmissionBeforeValidator(t *testing.T) {
	for _, kind := range []string{"nil-context", "nil-store", "canceled", "readonly", "disabled", "stale", "stale-revision", "typednil"} {
		t.Run(kind, func(t *testing.T) {
			s, path, key, a, b, state := activationRevisionFixture(t)
			ctx := context.Background()
			var calls int
			validator := Validator(ValidatorFunc(func(context.Context, Version) (Evidence, error) {
				calls++
				return Evidence{ID: "check", Passed: true, Deterministic: true}, nil
			}))
			target := s
			automatic := false
			expected := a
			switch kind {
			case "nil-context":
				ctx = nil
			case "nil-store":
				target = nil
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "readonly":
				var err error
				target, err = OpenReadOnly(path, []string{key.Scope})
				if err != nil {
					t.Fatal(err)
				}
				defer target.Close()
			case "disabled":
				automatic = true
			case "stale":
				expected = b
			case "stale-revision":
				state.Revision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			case "typednil":
				var nilCallback ValidatorFunc
				validator = nilCallback
			}
			before := activationCatalogBytes(t, path)
			var err error
			if kind == "stale-revision" {
				err = target.ActivateAt(ctx, state, b, validator, automatic)
			} else {
				err = target.Activate(ctx, key, b, expected, validator, automatic)
			}
			if err == nil || calls != 0 {
				t.Fatal("admission invoked validator", err, calls)
			}
			assertActivationCatalogUnchanged(t, path, before)
		})
	}
}

func TestActivationSafetyCallbackFailuresAreSanitized(t *testing.T) {
	for _, kind := range []string{"panic", "error", "kill-switch", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			s, path, _, _, b, state := activationRevisionFixture(t)
			s.SetAutomatic(true)
			before := activationCatalogBytes(t, path)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			validator := ValidatorFunc(func(context.Context, Version) (Evidence, error) {
				switch kind {
				case "panic":
					panic("private callback secret")
				case "error":
					return Evidence{}, errors.New("private callback secret")
				case "kill-switch":
					s.SetAutomatic(false)
				case "cancel":
					cancel()
				}
				return Evidence{ID: "check", Passed: true, Deterministic: true}, nil
			})
			err := s.ActivateAt(ctx, state, b, validator, true)
			if err == nil || (kind == "panic" || kind == "error") && !errors.Is(err, ErrValidation) {
				t.Fatal("unsafe callback outcome", err)
			}
			assertActivationCatalogUnchanged(t, path, before)
		})
	}
}

func TestActivationSafetyCooperativeDeadline(t *testing.T) {
	s, path, _, _, b, state := activationRevisionFixture(t)
	before := activationCatalogBytes(t, path)
	started := time.Now()
	err := s.ActivateAt(context.Background(), state, b, ValidatorFunc(func(ctx context.Context, _ Version) (Evidence, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 3*time.Second {
			t.Error("missing bounded deadline")
		}
		<-ctx.Done()
		return Evidence{ID: "check", Passed: true, Deterministic: true}, nil
	}), false)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 6*time.Second {
		t.Fatal("deadline not enforced", err)
	}
	assertActivationCatalogUnchanged(t, path, before)
}

func TestActivationSafetyValidatedCopyDoesNotRewriteVersion(t *testing.T) {
	s, _, key, _, b, state := activationRevisionFixture(t)
	before, err := s.Load(context.Background(), key, b)
	if err != nil {
		t.Fatal(err)
	}
	err = s.ActivateAt(context.Background(), state, b, ValidatorFunc(func(_ context.Context, v Version) (Evidence, error) {
		v.Draft.Steps[0] = "validator mutation"
		return Evidence{ID: "check", Passed: true, Deterministic: true}, nil
	}), false)
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.Load(context.Background(), key, b)
	if err != nil || after.Draft.Steps[0] != before.Draft.Steps[0] {
		t.Fatal("validator rewrote immutable version", err)
	}
	expectActiveVersion(t, s, key, b)
}
