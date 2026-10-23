package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"github.com/ArronJablonowski/NexusRouter/attachments"
	"github.com/ArronJablonowski/NexusRouter/internal/browserops"
	"github.com/ArronJablonowski/NexusRouter/webui"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
	"time"
)

func TestBrowserUploadsBoundToSubmissionAndChat(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, cfg := autoFixture(t)
	s.settings.WebUI.DefaultModel = "z"
	for i := range s.settings.Models {
		if s.settings.Models[i].ID == "a" {
			s.settings.Models[i].ID = "friendly-a"
		}
	}
	ops, e := browserops.Open(ctx, cfg.Telemetry.Database)
	if e != nil {
		t.Fatal(e)
	}
	defer ops.Close()
	b, _ := NewBrowserMutations(s, ops)
	files := []attachments.File{{Name: "revenue.csv", Data: []byte("category,value\nrevenue,76241\n")}, {Name: "example.go", Data: []byte("package example\nconst Value = 42\n")}}
	r := webui.ChatRequest{Version: 1, Action: webui.ChatSubmit, IdempotencyKey: "uploaded-files-example-01", ModelID: "friendly-a", Text: "Review these files", Files: files}
	receipt, e := b.Chat(ctx, browserMutationTestSubject, r)
	if e != nil {
		t.Fatal(e)
	}
	again, e := b.Chat(ctx, browserMutationTestSubject, r)
	if e != nil || again.SubmissionID != receipt.SubmissionID {
		t.Fatal("upload replay", again, e)
	}
	inputs, e := prepareChatInputs(ctx, files)
	if e != nil {
		t.Fatal(e)
	}
	request := Request{ModelID: "friendly-a", Prompt: attachments.Encode(r.Text, inputs), DisableFallback: true}
	dispatcher, e := StartDispatcher(ctx, s)
	if e != nil {
		t.Fatal(e)
	}
	defer dispatcher.Close()
	status, e := s.RunSubmission(ctx, receipt.OperationID, request)
	if e != nil || status.Result == nil {
		t.Fatal(status, e)
	}
	task := status.Result.TaskID
	page, e := s.ChatFiles(ctx, task)
	if e != nil || len(page.Files) != 2 {
		t.Fatal(page, e)
	}
	for _, f := range page.Files {
		_, body, e := s.ChatFile(ctx, task, f.ID)
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, original := range files {
			if f.Name == original.Name {
				found = bytes.Equal(body, original.Data)
			}
		}
		if !found {
			t.Fatal("original changed", f.Name)
		}
		if _, _, e = s.ChatFile(ctx, "other-chat", f.ID); e == nil {
			t.Fatal("cross chat upload read")
		}
	}
	changed := r
	changed.Files = []attachments.File{{Name: "revenue.csv", Data: []byte("changed")}}
	if _, e = b.Chat(ctx, browserMutationTestSubject, changed); e == nil {
		t.Fatal("changed file accepted under old key")
	}
	// Follow-up inherits the actual source model, even when the default changes.
	model, e := b.inheritedChatModel(ctx, webui.ChatRequest{ChatID: task, TaskID: task})
	if e != nil || model != "friendly-a" {
		t.Fatal("source model lost", model, e)
	}
	snapshot, e := InspectTask(ctx, cfg.Telemetry.Database, task)
	if e != nil {
		t.Fatal(e)
	}
	follow := webui.ChatRequest{Version: 1, Action: webui.ChatResume, IdempotencyKey: "uploaded-followup-key-02", ChatID: task, TaskID: task, ExpectedRevision: &snapshot.Sequence, Text: "Read this extra file", Files: []attachments.File{{Name: "follow.txt", Data: []byte("follow-up data 19")}}}
	followReceipt, e := b.Chat(ctx, browserMutationTestSubject, follow)
	if e != nil {
		t.Fatal(e)
	}
	followed := awaitSubmission(t, ctx, s, followReceipt.SubmissionID, "succeeded")
	if followed.Result == nil || followed.Result.Text != "a" {
		t.Fatal("follow-up changed model", followed)
	}
	page, e = s.ChatFiles(ctx, task)
	if e != nil || len(page.Files) != 3 {
		t.Fatal("follow-up originals missing", page, e)
	}

}
func TestUploadImagePreparationAndBadFiles(t *testing.T) {
	var p bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 48, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 48; x++ {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	png.Encode(&p, img)
	files, e := prepareChatInputs(context.Background(), []attachments.File{{Name: "photo.png", Data: p.Bytes()}})
	if e != nil || len(files) != 1 || files[0].Image == "" {
		t.Fatal(files, e)
	}
	s, _ := autoFixture(t)
	if !s.chatImageModelAllowed("a", files) || s.chatImageModelAllowed("auto", files) {
		t.Fatal("image model boundary")
	}
	for _, f := range []attachments.File{{Name: "old.doc", Data: []byte{0xd0, 0xcf, 0x11, 0}}, {Name: "broken.docx", Data: []byte("no zip")}, {Name: "broken.png", Data: []byte("not pixels")}} {
		if _, e := prepareChatInputs(context.Background(), []attachments.File{f}); !errors.Is(e, attachments.ErrFile) {
			t.Fatal("bad file accepted", f.Name, e)
		}
	}
}

func TestBrowserRejectsImageForAutomaticOrUnsupportedModel(t *testing.T) {
	ctx := context.Background()
	s, cfg := autoFixture(t)
	ops, e := browserops.Open(ctx, cfg.Telemetry.Database)
	if e != nil {
		t.Fatal(e)
	}
	defer ops.Close()
	b, _ := NewBrowserMutations(s, ops)
	var data bytes.Buffer
	png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	r := webui.ChatRequest{Version: 1, Action: webui.ChatSubmit, IdempotencyKey: "image-automatic-rejected-01", ModelID: "auto", Text: "Describe", Files: []attachments.File{{Name: "image.png", Data: data.Bytes()}}}
	if _, e = b.Chat(ctx, browserMutationTestSubject, r); !errors.Is(e, attachments.ErrFile) || !errors.Is(e, ErrAdmission) {
		t.Fatal("automatic image dispatch not definitively rejected", e)
	}
	// Rejected operations retain their definitive disposition across a retry.
	if _, e = b.Chat(ctx, browserMutationTestSubject, r); !errors.Is(e, ErrAdmission) {
		t.Fatal("rejected upload became uncertain", e)
	}
	s.settings.Providers[0].Kind = "codex_cli"
	r.ModelID = "a"
	r.IdempotencyKey = "image-unsupported-rejected-02"
	if _, e = b.Chat(ctx, browserMutationTestSubject, r); !errors.Is(e, attachments.ErrFile) {
		t.Fatal("unsupported image transport admitted", e)
	}
}

func TestUploadNoisyImageFitsConservativeContext(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 512, 384))
	var seed uint32 = 17
	for y := 0; y < 384; y++ {
		for x := 0; x < 512; x++ {
			seed = 1664525*seed + 1013904223
			img.Set(x, y, color.RGBA{R: byte(seed >> 24), G: byte(seed >> 16), B: byte(seed >> 8), A: 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	encoded, err := chatInputImage(b.Bytes())
	if err != nil || len(encoded) > 5464 || len(encoded) == 0 {
		t.Fatalf("image context budget: bytes=%d err=%v", len(encoded), err)
	}
}

func TestUploadTransparentImageKeepsBlackContentVisible(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := 20; y < 44; y++ {
		for x := 20; x < 44; x++ {
			img.SetNRGBA(x, y, color.NRGBA{A: 255})
		}
	}
	var p bytes.Buffer
	png.Encode(&p, img)
	encoded, err := chatInputImage(p.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(encoded)
	view, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	r, _, _, _ := view.At(0, 0).RGBA()
	center, _, _, _ := view.At(32, 32).RGBA()
	if r < 60000 || center > 5000 {
		t.Fatal("transparent black image lost contrast")
	}
}
