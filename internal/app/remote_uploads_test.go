package app

import (
	"bytes"
	"context"
	"github.com/ArronJablonowski/NexusRouter/attachments"
	"image"
	"image/png"
	"strings"
	"testing"
)

func TestRemoteUploadPreparationRedactsAndRejectsImages(t *testing.T) {
	s := submissionService(t)
	s.secret = func(string) string { return `synthetic<&"credential` }
	out, e := s.PrepareRemoteUploads(context.Background(), "Read", []attachments.File{{Name: "notes.txt", Data: []byte(`value synthetic<&"credential`)}})
	_, files := attachments.Decode(out)
	if e != nil || len(files) != 1 || strings.Contains(files[0].Text, `synthetic<&"credential`) {
		t.Fatal("remote redaction failed", e)
	}
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 3, 3)))
	if _, e = s.PrepareRemoteUploads(context.Background(), "Read", []attachments.File{{Name: "image.png", Data: b.Bytes()}}); e == nil {
		t.Fatal("unqualified image transport admitted")
	}
	if _, e = s.PrepareRemoteUploads(context.Background(), "Read", []attachments.File{{Name: "bad.doc", Data: []byte("not Word")}}); e == nil {
		t.Fatal("bad DOC admitted")
	}
}
