package skills

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRegressionRejectsInvalidActiveVersionBeforeValidator(t *testing.T) {
	for _, kind := range []string{"blank-step", "timestamp", "oversized-configuration"} {
		t.Run(kind, func(t *testing.T) {
			s, path, key, active, _, _ := activationRevisionFixture(t)
			ctx := context.Background()
			v, err := s.Load(ctx, key, active)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "blank-step":
				v.Draft.Steps = []string{" "}
			case "timestamp":
				v.CreatedAt = time.Time{}
			case "oversized-configuration":
				v.Draft.Configuration = strings.Repeat("x", 257<<10)
			}
			if !v.Draft.valid() || v.Validate() == nil {
				t.Fatal("fixture does not isolate stronger version validation")
			}
			body, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(path, "version-"+active+".json"), body, 0600); err != nil {
				t.Fatal(err)
			}
			// Recompute metadata as a legacy writer could, so the ordinary loader
			// accepts integrity while the callback contract rejects the payload.
			if err = s.with(ctx, func(c *catalog) error {
				e := c.Skills[key.index()]
				for i := range e.Versions {
					if e.Versions[i].Version == active {
						e.Versions[i].Digest = digest(v)
					}
				}
				c.Skills[key.index()] = e
				return nil
			}, true); err != nil {
				t.Fatal(err)
			}
			if _, err = s.Load(ctx, key, active); err != nil {
				t.Fatal("loader rejected fixture before new guard", err)
			}
			state, err := s.ActivationState(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			before := activationCatalogBytes(t, path)
			s.SetAutomatic(true)
			calls := 0
			result, err := s.RevalidateAndRollback(ctx, state, ValidatorFunc(func(context.Context, Version) (Evidence, error) {
				calls++
				return Evidence{ID: "check", Deterministic: true}, nil
			}))
			if !errors.Is(err, ErrValidation) || calls != 0 || !reflect.DeepEqual(result, RegressionResult{}) {
				t.Fatal("invalid version reached validator", result, err, calls)
			}
			assertActivationCatalogUnchanged(t, path, before)
		})
	}
}
