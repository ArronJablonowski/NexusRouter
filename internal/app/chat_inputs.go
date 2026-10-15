package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ArronJablonowski/NexusRouter/attachments"
	"github.com/ArronJablonowski/NexusRouter/internal/chatfiles"
	"github.com/ArronJablonowski/NexusRouter/internal/documenttext"
	"github.com/ArronJablonowski/NexusRouter/internal/processaudit"
	"github.com/ArronJablonowski/NexusRouter/internal/telemetry"
	"github.com/ArronJablonowski/NexusRouter/webui"
)

func prepareChatInputs(ctx context.Context, files []attachments.File) ([]attachments.Input, error) {
	ctx, cancel := context.WithTimeout(ctx, 22*time.Second)
	defer cancel()
	if attachments.Validate(files) != nil {
		return nil, attachments.ErrFile
	}
	var out []attachments.Input
	total := 0
	for _, f := range files {
		if ctx.Err() != nil {
			return nil, attachments.ErrProcessing
		}
		v := attachments.Input{Name: f.Name}
		ext := strings.ToLower(filepath.Ext(f.Name))
		var e error
		switch ext {
		case ".pdf", ".doc":
			if ext == ".doc" && (len(f.Data) < 512 || !bytes.HasPrefix(f.Data, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1})) {
				return nil, attachments.ErrFile
			}
			if ext == ".pdf" && !bytes.HasPrefix(f.Data, []byte("%PDF-")) {
				return nil, attachments.ErrFile
			}
			v.Text, e = extractDocumentChild(ctx, ext[1:], f.Data)
		case ".docx":
			v.Text, e = documenttext.DOCX(f.Data)
		case ".png", ".jpg", ".jpeg", ".gif":
			v.Image, e = chatInputImage(f.Data)
		default:
			if !utf8.Valid(f.Data) || bytes.IndexByte(f.Data, 0) >= 0 {
				return nil, attachments.ErrFile
			}
			v.Text = string(f.Data)
		}
		if errors.Is(e, attachments.ErrProcessing) {
			return nil, e
		}
		total += len(v.Text) + len(v.Image)
		if e != nil || len(v.Text) > attachments.MaxTextBytes || total > attachments.MaxInputBytes {
			return nil, attachments.ErrFile
		}
		out = append(out, v)
	}
	return out, nil
}
func extractDocumentChild(ctx context.Context, kind string, data []byte) (string, error) {
	binary, e := os.Executable()
	if e != nil {
		return "", e
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "internal-file-text", kind)
	dir, e := os.MkdirTemp("", "nexus-document-")
	if e != nil {
		return "", attachments.ErrProcessing
	}
	defer os.RemoveAll(dir)
	cmd.Env = append(os.Environ(), "TMPDIR="+dir)
	cmd.Dir = dir
	defer configureDocumentProcess(cmd)()
	cmd.Stdin = bytes.NewReader(data)
	var out cappedDocumentWriter
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if e = processaudit.Run(cmd); e != nil {
		var exit *exec.ExitError
		if ctx.Err() != nil || errors.As(e, &exit) && exit.ExitCode() == 2 {
			return "", attachments.ErrProcessing
		}
		return "", attachments.ErrFile
	}
	if out.buffer.Len() == 0 || !utf8.Valid(out.buffer.Bytes()) {
		return "", attachments.ErrFile
	}
	return out.buffer.String(), nil
}

type cappedDocumentWriter struct{ buffer bytes.Buffer }

func (w *cappedDocumentWriter) Write(b []byte) (int, error) {
	if w.buffer.Len()+len(b) > documenttext.MaxText {
		return 0, attachments.ErrFile
	}
	return w.buffer.Write(b)
}
func chatInputImage(data []byte) (string, error) {
	c, _, e := image.DecodeConfig(bytes.NewReader(data))
	if e != nil || c.Width < 1 || c.Height < 1 || int64(c.Width)*int64(c.Height) > 16_000_000 {
		return "", attachments.ErrFile
	}
	src, _, e := image.Decode(bytes.NewReader(data))
	if e != nil {
		return "", attachments.ErrFile
	}
	for size := 512; size >= 64; size /= 2 {
		width, height := c.Width, c.Height
		if width > size || height > size {
			if width >= height {
				height = max(1, height*size/width)
				width = size
			} else {
				width = max(1, width*size/height)
				height = size
			}
		}
		dst := image.NewRGBA(image.Rect(0, 0, width, height))
		b := src.Bounds()
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				r, g, blue, a := src.At(b.Min.X+x*c.Width/width, b.Min.Y+y*c.Height/height).RGBA()
				dst.SetRGBA(x, y, color.RGBA{R: uint8((r + 65535 - a) >> 8), G: uint8((g + 65535 - a) >> 8), B: uint8((blue + 65535 - a) >> 8), A: 255})
			}
		}
		var encoded bytes.Buffer
		if jpeg.Encode(&encoded, dst, &jpeg.Options{Quality: 70}) != nil {
			return "", attachments.ErrFile
		}
		if encoded.Len() <= 4<<10 {
			return base64.StdEncoding.EncodeToString(encoded.Bytes()), nil
		}
	}
	return "", attachments.ErrFile
}
func (s *Service) retainChatInputs(ctx context.Context, operation string, files []attachments.File) error {
	if len(files) == 0 {
		return nil
	}
	store, e := chatfiles.Open(ctx, s.settings.Telemetry.Database+".chat-inputs")
	if e != nil {
		return e
	}
	defer store.Close()
	for _, f := range files {
		kind := http.DetectContentType(f.Data)
		if kind != "image/png" && kind != "image/jpeg" {
			kind = "application/octet-stream"
		}
		meta := webui.ChatFile{ID: chatfiles.ID(operation, f.Name), TaskID: operation, Name: f.Name, Size: len(f.Data), MediaType: kind}
		if e = store.Put(ctx, operation, meta, f.Data); e != nil {
			return e
		}
	}
	return store.BindInputs(ctx, "key_"+submissionDigest([]byte(operation)), operation)
}
func (s *Service) collectChatInputs(ctx context.Context, db *telemetry.Store, store *chatfiles.Store, chat, task, submission string) error {
	if submission == "" {
		return nil
	}
	inputs, e := chatfiles.Open(ctx, s.settings.Telemetry.Database+".chat-inputs")
	if e != nil {
		return e
	}
	defer inputs.Close()
	key, e := db.SubmissionInputKey(ctx, submission)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	op, e := inputs.InputOperation(ctx, "key_"+key)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	files, e := inputs.List(ctx, op)
	if e != nil {
		return e
	}
	for _, f := range files {
		_, data, e := inputs.Get(ctx, op, f.ID)
		if e != nil {
			return e
		}
		f.ID = chatfiles.ID(chat, "input/"+submission+"/"+f.ID)
		f.TaskID = task
		if _, _, already := store.Get(ctx, chat, f.ID); already == nil {
			continue
		}
		if e = store.Put(ctx, chat, f, data); e != nil {
			return e
		}
	}
	return nil
}

func (s *Service) chatImageModelAllowed(model string, files []attachments.Input) bool {
	images := false
	for _, f := range files {
		images = images || f.Image != ""
	}
	if !images {
		return true
	}
	for _, m := range s.settings.Models {
		if m.ID == model {
			for _, p := range s.settings.Providers {
				if p.ID == m.Provider {
					return p.Kind == "ollama" || p.Kind == "openai_compatible"
				}
			}
		}
	}
	return false
}
func (b *BrowserMutations) inheritedChatModel(ctx context.Context, r webui.ChatRequest) (string, error) {
	db, release, e := b.service.openTaskReadStore(ctx)
	if e != nil {
		return "", e
	}
	defer release()
	events, e := db.Read(ctx, r.TaskID, 0, 1)
	if e != nil || len(events) != 1 || events[0].SessionID != r.ChatID || events[0].Data.ModelID == "" {
		return "", ErrAdmission
	}
	source := events[0].Data
	inherited := ""
	for _, model := range b.service.settings.Models {
		if model.Model == source.ModelID && model.Provider == source.ProviderID {
			if inherited != "" {
				return "", ErrAdmission
			}
			inherited = model.ID
		}
	}
	if inherited == "" {
		return "", ErrAdmission
	}
	return inherited, nil
}
