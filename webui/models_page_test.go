package webui

import (
	"strings"
	"testing"
)

func TestEmbeddedModelInventoryIsDynamicBoundedAndReadOnly(t *testing.T) {
	script, err := embeddedShellAssets.ReadFile("assets/v1/models.js")
	if err != nil {
		t.Fatal(err)
	}
	body := string(script)
	for _, required := range []string{
		`fetch(base + "/api/v1/models"`, `const intervalMS = 10000`, `document.addEventListener("visibilitychange"`,
		`if (loading || stopped || document.hidden) return`, `duplicate model digests are counted once`, `local_total_bytes`,
		`local_unknown_size_count`, `Showing the last verified snapshot`, `credentials: "same-origin"`, `cache: "no-store"`,
		`node.textContent = value`, `locality === "local"`, `locality === "cloud"`, `const expanded = new Set()`,
		`heading.setAttribute("aria-expanded", String(open))`, `details.hidden = !open`, `expanded.add(item.id)`,
		`expanded.delete(item.id)`, `if (!present.has(id)) expanded.delete(id)`, `model-status-badge`, `model-install-badge`,
		`model-config-badge`, `model-size-badge`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("model inventory client lost required guard %q", required)
		}
	}
	for _, forbidden := range []string{"innerHTML", "api_key", "Authorization", "POST", "DELETE", "PUT"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("model inventory client contains unsafe primitive %q", forbidden)
		}
	}
	markup := string(mustAsset(t, "assets/v1/index.html"))
	for _, required := range []string{`data-view="models"`, `id="models-view"`, `id="local-model-total"`, `id="local-model-list"`, `id="cloud-model-list"`, `/assets/v1/models.js`} {
		if !strings.Contains(markup, required) {
			t.Fatalf("model inventory markup missing %q", required)
		}
	}
	styles := string(mustAsset(t, "assets/v1/app.css"))
	for _, required := range []string{
		`.model-card-toggle { display: grid; grid-template-columns: minmax(8rem, 1fr) 19.56rem 1.2rem;`,
		`.model-badges { display: grid; grid-template-columns: 5rem 4.4rem 5rem 4.5rem;`,
		`.model-badge { justify-self: center;`,
		`.model-status-badge { grid-column: 1; }`, `.model-install-badge { grid-column: 2; }`,
		`.model-config-badge { grid-column: 3; }`, `.model-size-badge { grid-column: 4;`,
		`.model-badges { display: flex; grid-column: 1 / -1; grid-row: 2;`,
	} {
		if !strings.Contains(styles, required) {
			t.Fatalf("model inventory styling lost aligned metadata contract %q", required)
		}
	}
}
