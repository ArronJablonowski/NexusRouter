package webui

import (
	"strings"
	"testing"
)

func TestRoutingAndEliminationPagesUseLiveBoundedEvidence(t *testing.T) {
	markup := string(mustAsset(t, "assets/v1/index.html"))
	script := string(mustAsset(t, "assets/v1/routing-map.js"))
	styles := string(mustAsset(t, "assets/v1/app.css"))
	for _, required := range []string{`data-view="routing"`, `data-view="elimination"`, `id="routing-network"`, `id="routing-branches"`, `id="commander-model"`, `id="commander-fallback"`, `id="resource-guard"`, `id="specialist-policy"`, `id="specialist-grid"`, `id="creative-question"`, `id="elimination-candidates"`, `/assets/v1/routing-map.js`} {
		if !strings.Contains(markup, required) {
			t.Fatalf("routing page markup missing %q", required)
		}
	}
	for _, required := range []string{`fetch(base + "/api/v1/models"`, `fetch(base + "/api/v1/models/deprecation?"`, `row.models.length <= 3`, `snapshot.specialists_allow_cloud || model.locality === "local"`, `snapshot.rankings || []`, `learned(model,job)`, `routing score`, `context ceiling`, `Selected context`, `Advertised maximum`, `element("details","route-model-details")`, `expandedModels.has(expansionKey)`, `window.history.replaceState`, `DarwinRouter ID`, `Estimated RAM`, `routing-flow routing-flow-`, `window.setTimeout(loadRouting`, `document.addEventListener("visibilitychange"`, `No model crosses the evidence threshold`, `No recommendation was manufactured`, `credentials:"same-origin"`, `cache:"no-store"`} {
		if !strings.Contains(script, required) {
			t.Fatalf("routing client lost required behavior %q", required)
		}
	}
	for _, required := range []string{`Local endpoints only`, `The commander asks`} {
		if !strings.Contains(markup, required) {
			t.Fatalf("routing page copy missing %q", required)
		}
	}
	for _, forbidden := range []string{"innerHTML", "localStorage", "sessionStorage", "Authorization", "DELETE", "PUT"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("routing client contains unsafe primitive %q", forbidden)
		}
	}
	for _, required := range []string{`.tron-view {`, `.commander-core {`, `.routing-branches {`, `.routing-trunk { stroke:#dcffff; stroke-width:5;`, `.routing-flow-out {`, `.routing-flow-in {`, `@keyframes routing-data-out`, `@keyframes routing-data-in`, `.routing-flow { display:none; animation:none; }`, `.specialist-grid {`, `grid-template-columns:repeat(2`, `.route-model-details{`, `.route-model-facts{`, `.elimination-layout{`, `@media (prefers-reduced-motion: reduce)`} {
		if !strings.Contains(styles, required) {
			t.Fatalf("TRON presentation missing %q", required)
		}
	}
}
