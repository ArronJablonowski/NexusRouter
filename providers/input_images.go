package providers

import (
	"encoding/json"
	"github.com/ArronJablonowski/NexusRouter/attachments"
)

// Preserve durable upload framing in history while translating image bytes at
// the HTTP boundary. Only user messages become multimodal; no URLs are fetched.
func applyInputImages(wire map[string]any, content, kind string) {
	text, files := attachments.Decode(content)
	var images []string
	for i, f := range files {
		if f.Image != "" {
			images = append(images, f.Image)
			files[i].Image = ""
			files[i].Text = "Image attached for visual analysis."
		}
	}
	if len(images) == 0 {
		return
	}
	wire["content"] = attachments.Encode(text, files)
	if kind == "ollama" {
		wire["images"] = images
		return
	}
	parts := []any{map[string]any{"type": "text", "text": wire["content"]}}
	for _, data := range images {
		parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:image/jpeg;base64," + data}})
	}
	// Ensure no non-JSON values reach an adapter request.
	if _, e := json.Marshal(parts); e == nil {
		wire["content"] = parts
	}
}
