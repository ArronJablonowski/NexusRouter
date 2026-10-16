package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"github.com/ArronJablonowski/NexusRouter/attachments"
	"github.com/ArronJablonowski/NexusRouter/providers"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func TestRemoteUploadPreparationRedactsAndPreservesImages(t *testing.T) {
	s := submissionService(t)
	s.secret = func(string) string { return `synthetic<&"credential` }
	out, e := s.PrepareRemoteUploads(context.Background(), "Read", []attachments.File{{Name: "notes.txt", Data: []byte(`value synthetic<&"credential`)}})
	_, files := attachments.Decode(out)
	if e != nil || len(files) != 1 || strings.Contains(files[0].Text, `synthetic<&"credential`) {
		t.Fatal("remote redaction failed", e)
	}
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 3, 3)))
	out, e = s.PrepareRemoteUploads(context.Background(), "Read", []attachments.File{{Name: "image.png", Data: b.Bytes()}})
	_, files = attachments.Decode(out)
	expected, _ := chatInputImage(b.Bytes())
	if e != nil || len(files) != 1 || files[0].Image != expected {
		t.Fatal("remote image damaged", e)
	}
	if _, e = s.PrepareRemoteUploads(context.Background(), "Read", []attachments.File{{Name: "bad.doc", Data: []byte("not Word")}}); e == nil {
		t.Fatal("bad DOC admitted")
	}
}

func TestRemoteUploadImagesFitCombinedConservativeContext(t *testing.T) {
	s := submissionService(t)
	im := image.NewRGBA(image.Rect(0, 0, 320, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 320; x++ {
			c := color.RGBA{255, 255, 255, 255}
			if x > y/2 && x < 320-y/2 {
				c = color.RGBA{135, 40, 190, 255}
			}
			im.SetRGBA(x, y, c)
		}
	}
	var raw bytes.Buffer
	png.Encode(&raw, im)
	files := []attachments.File{{Name: "a.png", Data: raw.Bytes()}, {Name: "b.png", Data: raw.Bytes()}, {Name: "c.png", Data: raw.Bytes()}}
	out, e := s.PrepareRemoteUploads(context.Background(), "Describe these three images", files)
	if e != nil {
		t.Fatal(e)
	}
	estimate, e := providers.EstimateContext(providers.Request{Messages: []providers.Message{{Role: "user", Content: out}}})
	if e != nil || estimate > 8192 {
		t.Fatalf("image batch exceeds conservative context: %d %v", estimate, e)
	}
	_, inputs := attachments.Decode(out)
	total := 0
	for _, f := range inputs {
		b, e := base64.StdEncoding.DecodeString(f.Image)
		if e != nil || len(b) == 0 {
			t.Fatal("image missing")
		}
		total += len(b)
	}
	if len(inputs) != 3 || total > 4<<10 {
		t.Fatal("combined analysis budget exceeded", len(inputs), total)
	}
}
