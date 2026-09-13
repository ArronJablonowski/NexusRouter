package webui

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var (
	htmlIDPattern        = regexp.MustCompile(`\bid="([^"]+)"`)
	ariaReferencePattern = regexp.MustCompile(`\baria-(?:labelledby|describedby)="([^"]+)"`)
	controlPattern       = regexp.MustCompile(`(?is)<(input|select|textarea)\b([^>]*)>`)
	attributePattern     = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9_-]*)="([^"]*)"`)
)

func TestEmbeddedShellStaticAccessibilityContract(t *testing.T) {
	body, err := embeddedShellAssets.ReadFile("assets/v1/index.html")
	if err != nil {
		t.Fatal(err)
	}
	markup := string(body)
	ids := map[string]bool{}
	for _, match := range htmlIDPattern.FindAllStringSubmatch(markup, -1) {
		if ids[match[1]] {
			t.Fatalf("duplicate HTML id %q", match[1])
		}
		ids[match[1]] = true
	}
	for _, match := range ariaReferencePattern.FindAllStringSubmatch(markup, -1) {
		for _, id := range strings.Fields(match[1]) {
			if !ids[id] {
				t.Fatalf("ARIA reference targets missing id %q", id)
			}
		}
	}
	for _, match := range controlPattern.FindAllStringSubmatchIndex(markup, -1) {
		tag, source := markup[match[2]:match[3]], markup[match[4]:match[5]]
		attrs := accessibilityAttributes(source)
		if tag == "input" && attrs["type"] == "hidden" {
			continue
		}
		id := attrs["id"]
		before := markup[:match[0]]
		wrapped := strings.LastIndex(before, "<label") > strings.LastIndex(before, "</label>") && strings.Contains(markup[match[1]:], "</label>")
		explicit := id != "" && strings.Contains(markup, `for="`+id+`"`)
		if !explicit && !wrapped && attrs["aria-label"] == "" && attrs["aria-labelledby"] == "" {
			t.Fatalf("visible %s %q has no accessible label", tag, id)
		}
	}
	for _, required := range []string{
		`<a class="skip-link" href="#main">Skip to content</a>`, `<main id="main" tabindex="-1">`,
		`<nav class="rail" aria-label="Primary">`, `role="dialog" aria-modal="true"`,
		`role="alertdialog" aria-modal="true"`, `role="status" aria-live="polite"`,
	} {
		if !strings.Contains(markup, required) {
			t.Fatalf("accessibility contract missing %q", required)
		}
	}
	if regexp.MustCompile(`tabindex="[1-9]`).MatchString(markup) {
		t.Fatal("positive tabindex changes the natural keyboard order")
	}
}

func TestEmbeddedShellTextContrastMeetsWCAGAA(t *testing.T) {
	styles, err := embeddedShellAssets.ReadFile("assets/v1/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(styles)
	pairs := []struct {
		name, foreground, background string
	}{
		{"muted text on page", "#9fb0ce", "#0b1020"},
		{"muted text on panel", "#9fb0ce", "#111a2d"},
		{"secondary text on panel", "#cbd6e9", "#111a2d"},
		{"empty lane text", "#7586a5", "#0d1628"},
		{"advisory evidence", "#7f8ba3", "#17223a"},
		{"warning text", "#ffcf70", "#0b1020"},
		{"error text", "#ffb6b6", "#0b1020"},
		{"secondary button", "#e8edf8", "#1b2943"},
		{"primary button", "#08130f", "#73e6b1"},
		{"danger button", "#fff", "#5d2730"},
	}
	for _, pair := range pairs {
		if !strings.Contains(css, pair.foreground) || !strings.Contains(css, pair.background) {
			t.Fatalf("%s uses an untracked color token", pair.name)
		}
		if ratio := contrastRatio(pair.foreground, pair.background); ratio < 4.5 {
			t.Fatalf("%s contrast %.2f is below WCAG AA 4.5:1", pair.name, ratio)
		}
	}
	if !strings.Contains(css, "#66799b") {
		t.Fatal("form control boundary color is not tracked")
	}
	for _, background := range []string{"#0d1628", "#111a2d"} {
		if ratio := contrastRatio("#66799b", background); ratio < 3 {
			t.Fatalf("form control boundary contrast %.2f is below WCAG non-text 3:1", ratio)
		}
	}
	if !strings.Contains(css, `@media (prefers-reduced-motion: reduce)`) || !strings.Contains(css, `:focus-visible { outline: 3px solid #73e6b1;`) {
		t.Fatal("reduced-motion or visible-focus accommodation missing")
	}
}

func accessibilityAttributes(source string) map[string]string {
	result := map[string]string{}
	for _, match := range attributePattern.FindAllStringSubmatch(source, -1) {
		result[strings.ToLower(match[1])] = match[2]
	}
	return result
}

func contrastRatio(foreground, background string) float64 {
	a, b := relativeLuminance(foreground), relativeLuminance(background)
	return (math.Max(a, b) + 0.05) / (math.Min(a, b) + 0.05)
}

func relativeLuminance(color string) float64 {
	value := strings.TrimPrefix(color, "#")
	if len(value) == 3 {
		value = string([]byte{value[0], value[0], value[1], value[1], value[2], value[2]})
	}
	channels := make([]float64, 3)
	for index := range channels {
		part, err := strconv.ParseUint(value[index*2:index*2+2], 16, 8)
		if err != nil {
			return 0
		}
		channel := float64(part) / 255
		if channel <= 0.04045 {
			channels[index] = channel / 12.92
		} else {
			channels[index] = math.Pow((channel+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*channels[0] + 0.7152*channels[1] + 0.0722*channels[2]
}
