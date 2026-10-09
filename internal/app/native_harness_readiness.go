package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"slices"

	"github.com/ArronJablonowski/NexusRouter/harness"
	"github.com/ArronJablonowski/NexusRouter/internal/config"
)

// NativeHarnessReadiness observes file pins, credential presence and provider
// inventory, without running a harness or inference. The remote host must check
// paired scope before invoking this method. No paths, secrets or raw errors are
// returned. Runtime version/dependency and actual tool checks remain at execution.
func (s *Service) NativeHarnessReadiness(ctx context.Context, modelID, registration string, tokens int) (out harness.Readiness, err error) {
	defer func() {
		if recover() != nil {
			out = harness.Readiness{}
			err = ErrAdmission
		}
	}()
	if s == nil || ctx == nil || ctx.Err() != nil {
		return out, ErrAdmission
	}
	if registration == harness.DirectRegistration(modelID) {
		return s.directReadiness(ctx, modelID, tokens)
	}
	identity, err := s.NativeHarnessIdentity(modelID, registration, tokens)
	if err != nil {
		return out, err
	}
	entry := s.nativeHarnesses[registration]
	var model config.Model
	var provider config.Provider
	for _, m := range s.settings.Models {
		if m.ID == modelID {
			model = m
			break
		}
	}
	for _, p := range s.settings.Providers {
		if p.ID == model.Provider {
			provider = p
			break
		}
	}
	toolContract := nativeToolsFor(s.settings, s.toolExtension)
	out = harness.Readiness{Identity: identity, CredentialState: "not_required", ModelState: "unknown", Local: model.Locality == "local", Capabilities: slices.Clone(model.Capabilities), ContextTokens: int64(model.ContextTokens), Compatible: model.EstimatedCost != nil && (len(toolContract.Catalog) == 0 || entry.NativeTools) && (!entry.NativeTools || model.Locality == "local")}
	if model.EstimatedCost != nil {
		out.EstimatedCost = *model.EstimatedCost
	}
	if (s.settings.Mode == "local_only" && !out.Local) || (s.settings.Mode == "cloud_only" && out.Local) {
		out.Compatible = false
	}
	out.ExecutableMatched = nativeArtifactMatchesContext(ctx, entry)
	if provider.APIKeyEnv != "" {
		out.CredentialState = "missing"
		if s.secret != nil && s.secret(provider.APIKeyEnv) != "" {
			out.CredentialState = "present"
		}
	}
	// Missing credentials or a failed local prerequisite never cause a provider
	// request merely to produce another negative observation.
	if out.ExecutableMatched && out.Compatible && out.CredentialState != "missing" {
		models, e := s.nativeModelInventory(ctx, provider, out.Local)
		if e == nil {
			out.ModelState = "absent"
			if models[model.Model] {
				out.ModelState = "present"
			}
		}
	}
	if ctx.Err() != nil {
		return harness.Readiness{}, ctx.Err()
	}
	if out.Validate() != nil {
		return harness.Readiness{}, ErrAdmission
	}
	return out, nil
}
func nativeArtifactMatchesContext(ctx context.Context, entry NativeHarness) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	st, err := os.Stat(entry.Executable)
	if err != nil || !st.Mode().IsRegular() || st.Size() > 256<<20 {
		return false
	}
	f, err := os.Open(entry.Executable)
	if err != nil {
		return false
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(st, actual) {
		return false
	}
	h := sha256.New()
	buf := make([]byte, 64<<10)
	total := 0
	for {
		if ctx.Err() != nil {
			return false
		}
		n, e := f.Read(buf)
		total += n
		if total > 256<<20 {
			return false
		}
		if n > 0 {
			h.Write(buf[:n])
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return false
		}
	}
	return hex.EncodeToString(h.Sum(nil)) == entry.ExecutableSHA256
}
