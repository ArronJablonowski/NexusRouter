package webuiapp

import (
	"context"
	"net/http"
	"time"
)

type ModelRegistration struct {
	Version        int    `json:"version"`
	ExpectedDigest string `json:"expected_digest"`
	CatalogID      string `json:"catalog_id"`
	TemplateID     string `json:"template_id"`
	ContextTokens  int    `json:"context_tokens"`
}

func (h *Handler) serveModelRegistration(w http.ResponseWriter, r *http.Request) {
	if !h.requireMutationAuthority(w, r) {
		return
	}
	if r.Method != http.MethodPost || !mutationSlot(h, false) {
		h.writeError(w, r, 503, "configuration_unavailable")
		return
	}
	defer releaseMutationSlot(h, false)
	var input ModelRegistration
	if decodeMutationJSON(r, &input, 4096) != nil || input.Version != 1 || len(input.ExpectedDigest) != 64 || len(input.CatalogID) > 128 || len(input.TemplateID) > 128 || input.ContextTokens < 1 || input.ContextTokens > 1<<24 || h.mutations.RegisterModel == nil {
		h.writeError(w, r, 400, "invalid_request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	digest, e := safeCall(func() (string, error) { return h.mutations.RegisterModel(ctx, input) })
	if e != nil {
		h.writeError(w, r, 409, "model_configuration_changed_or_unavailable")
		return
	}
	h.writeJSON(w, 200, struct {
		Version         int    `json:"version"`
		Digest          string `json:"digest"`
		RestartRequired bool   `json:"restart_required"`
		Enabled         bool   `json:"enabled"`
	}{1, digest, true, false})
}
