package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/ArronJablonowski/NexusRouter/attachments"
	"net/http"
	"strings"
	"testing"
)

func TestUploadedImagesReachHTTPAdapters(t *testing.T) {
	image := base64.StdEncoding.EncodeToString([]byte{255, 216, 255, 1})
	prompt := attachments.Encode("Describe", []attachments.Input{{Name: "photo.jpg", Image: image}, {Name: "data.csv", Text: "revenue,42"}, {Name: "source.html", Text: strings.Repeat("<", 64<<10)}})
	for _, kind := range []string{"ollama", "openai_compatible"} {
		t.Run(kind, func(t *testing.T) {
			p := fixtureProvider(t, kind, func(w http.ResponseWriter, r *http.Request) {
				var v struct {
					Messages []map[string]any `json:"messages"`
				}
				if json.NewDecoder(r.Body).Decode(&v) != nil {
					t.Fatal("wire JSON")
				}
				m := v.Messages[0]
				if kind == "ollama" {
					images, ok := m["images"].([]any)
					if !ok || len(images) != 1 || images[0] != image || strings.Contains(m["content"].(string), image) {
						t.Error("image bytes not in Ollama image field", m)
					}
					fmt.Fprintln(w, `{"message":{"content":"visible"},"done":true,"done_reason":"stop"}`)
				} else {
					parts, ok := m["content"].([]any)
					if !ok || len(parts) != 2 {
						t.Error("multimodal content missing", m)
					} else if parts[1].(map[string]any)["image_url"].(map[string]any)["url"] != "data:image/jpeg;base64,"+image {
						t.Error("wrong image")
					}
					fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"visible\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n")
				}
			})
			if e := p.Stream(context.Background(), Request{Model: "fixture", Messages: []Message{{Role: "user", Content: prompt}}}, func(Chunk) error { return nil }); e != nil {
				t.Fatal(e)
			}
		})
	}
}
