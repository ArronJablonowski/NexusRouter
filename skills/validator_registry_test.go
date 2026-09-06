package skills

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type registryNilValidator struct{}

func (*registryNilValidator) Validate(context.Context, Version) (Evidence, error) {
	panic("must never invoke typed nil")
}

func TestValidatorRegistryCopiesMappingAndDoesNotInvoke(t *testing.T) {
	var calls atomic.Int32
	validator := ValidatorFunc(func(context.Context, Version) (Evidence, error) {
		calls.Add(1)
		return Evidence{ID: "proof", Passed: true, Deterministic: true}, nil
	})
	input := map[string]Validator{"stable-v1": validator}
	registry, err := NewValidatorRegistry(input)
	if err != nil {
		t.Fatal(err)
	}
	input["stable-v1"] = nil
	input["new-v2"] = validator
	delete(input, "stable-v1")
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Go(func() {
			for j := 0; j < 50; j++ {
				if got, err := registry.Resolve("stable-v1"); err != nil || got == nil {
					t.Error("copied mapping changed", err)
				}
			}
		})
	}
	group.Wait()
	if calls.Load() != 0 {
		t.Fatal("registry invoked callbacks")
	}
	if _, err := registry.Resolve("new-v2"); !errors.Is(err, ErrNotFound) {
		t.Fatal("caller map mutation changed registry", err)
	}
	resolved, err := registry.Resolve("stable-v1")
	if err != nil {
		t.Fatal(err)
	}
	proof, err := resolved.Validate(context.Background(), Version{})
	if err != nil || !proof.Passed || calls.Load() != 1 {
		t.Fatal("resolved wrong validator", err)
	}
}

func TestValidatorRegistryRejectsInvalidAndNilEntries(t *testing.T) {
	valid := ValidatorFunc(func(context.Context, Version) (Evidence, error) { return Evidence{}, nil })
	for _, id := range []string{"", "space name", "bad/name", "bad.name", strings.Repeat("x", 65), "\x00"} {
		if got, err := NewValidatorRegistry(map[string]Validator{id: valid}); !errors.Is(err, ErrInvalid) || got != nil {
			t.Fatal("invalid registry ID", err)
		}
	}
	var typedNil *registryNilValidator
	var nilFunction ValidatorFunc
	for _, validator := range []Validator{nil, typedNil, nilFunction} {
		if got, err := NewValidatorRegistry(map[string]Validator{"policy": validator}); !errors.Is(err, ErrInvalid) || got != nil {
			t.Fatal("nil callback admitted", err)
		}
	}
	entries := map[string]Validator{}
	for i := 0; i < 64; i++ {
		entries[fmt.Sprintf("validator-%d", i)] = valid
	}
	if _, err := NewValidatorRegistry(entries); err != nil {
		t.Fatal("64-entry registry rejected", err)
	}
	entries["overflow"] = valid
	if got, err := NewValidatorRegistry(entries); !errors.Is(err, ErrInvalid) || got != nil {
		t.Fatal("oversize registry admitted", err)
	}
	for _, input := range []map[string]Validator{nil, {}} {
		empty, err := NewValidatorRegistry(input)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := empty.Resolve("missing"); !errors.Is(err, ErrNotFound) || got != nil {
			t.Fatal("empty registry resolved policy", err)
		}
	}
	for _, registry := range []*ValidatorRegistry{nil, {}} {
		if got, err := registry.Resolve("policy"); err == nil || got != nil {
			t.Fatal("uninitialized registry resolved policy")
		}
	}
	registry, _ := NewValidatorRegistry(map[string]Validator{"policy": valid})
	if got, err := registry.Resolve("bad/name"); !errors.Is(err, ErrInvalid) || got != nil {
		t.Fatal("invalid lookup admitted", err)
	}
}
