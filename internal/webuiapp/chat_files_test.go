package webuiapp

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/webui"
)

func TestChatFileSessionScopeAndSafeDownloads(t *testing.T) {
	h := handlerFixture(t)
	cookie, _ := authenticateBrowser(t, h)
	var imageBody bytes.Buffer
	png.Encode(&imageBody, image.NewRGBA(image.Rect(0, 0, 8, 8)))
	file := webui.ChatFile{ID: "file-one", TaskID: "task", Name: "car.png", MediaType: "image/png", Size: imageBody.Len()}
	calls := 0
	h.reads.ChatFiles = func(_ context.Context, chat string) (webui.ChatFilePage, error) {
		calls++
		return webui.ChatFilePage{Version: 1, ChatID: chat, Files: []webui.ChatFile{file}}, nil
	}
	h.reads.ChatFile = func(_ context.Context, chat, id string) (webui.ChatFile, []byte, error) {
		calls++
		if chat != "chat" || id != file.ID {
			return webui.ChatFile{}, nil, errors.New("unavailable")
		}
		return file, imageBody.Bytes(), nil
	}
	get := func(path string, auth bool) *httptest.ResponseRecorder {
		r := browserGET(path, nil)
		if auth {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := get("/app/api/v1/chats/chat/files/file-one/download", false); w.Code != 401 || calls != 0 {
		t.Fatal("anonymous download", w.Code, calls)
	}
	if w := get("/app/api/v1/chats/chat/files", true); w.Code != 200 || !strings.Contains(w.Body.String(), "car.png") {
		t.Fatal(w.Code, w.Body.String())
	}
	w := get("/app/api/v1/chats/chat/files/file-one/preview", true)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || !bytes.Equal(w.Body.Bytes(), imageBody.Bytes()) {
		t.Fatal("image preview", w.Code)
	}
	w = get("/app/api/v1/chats/chat/files/file-one/download", true)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("unsafe download headers", w.Header())
	}
	for _, path := range []string{"/app/api/v1/chats/other/files/file-one/download", "/app/api/v1/chats/chat/files/unknown/download", "/app/api/v1/chats/chat/files/file-one/execute"} {
		if w := get(path, true); w.Code != 404 {
			t.Fatal(path, w.Code)
		}
	}
	if w := get("/app/api/v1/chats/chat/files?path=/etc/passwd", true); w.Code != 400 {
		t.Fatal("path selection", w.Code)
	}
	file.Name = "page.html"
	file.MediaType = "text/plain"
	imageBody.Reset()
	imageBody.WriteString("<script>alert(1)</script>")
	file.Size = imageBody.Len()
	if w := get("/app/api/v1/chats/chat/files/file-one/preview", true); w.Code != http.StatusUnsupportedMediaType {
		t.Fatal("active document preview", w.Code)
	}
	if w := get("/app/api/v1/chats/chat/files/file-one/download", true); w.Code != 200 || w.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatal("document download", w.Code)
	}
}
