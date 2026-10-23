package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/ArronJablonowski/NexusRouter/attachments"
)

func TestUploadPreparationTextAndImageMatrix(t *testing.T) {
	for _, name := range []string{"notes.md", "code.py", "data.csv", "data.json", "unicode.txt"} {
		data := []byte("héllo <>&\nreference,73192\n")
		files, err := prepareChatInputs(context.Background(), []attachments.File{{Name: name, Data: data}})
		if err != nil || len(files) != 1 || files[0].Text != string(data) {
			t.Fatal(name, err)
		}
	}
	img := image.NewPaletted(image.Rect(0, 0, 32, 32), color.Palette{color.White, color.Black})
	for _, kind := range []string{"png", "jpg", "gif"} {
		var b bytes.Buffer
		switch kind {
		case "png":
			png.Encode(&b, img)
		case "jpg":
			jpeg.Encode(&b, img, nil)
		case "gif":
			gif.Encode(&b, img, nil)
		}
		files, err := prepareChatInputs(context.Background(), []attachments.File{{Name: "picture." + kind, Data: b.Bytes()}})
		if err != nil || len(files) != 1 || files[0].Image == "" {
			t.Fatal(kind, err)
		}
	}
	for _, bad := range [][]byte{{255, 254}, {'a', 0, 'b'}, bytes.Repeat([]byte("a"), attachments.MaxTextBytes+1)} {
		if _, err := prepareChatInputs(context.Background(), []attachments.File{{Name: "file.txt", Data: bad}}); err == nil {
			t.Fatal("invalid text accepted")
		}
	}
	data := bytes.Repeat([]byte("<"), attachments.MaxTextBytes)
	files, err := prepareChatInputs(context.Background(), []attachments.File{{Name: "one.html", Data: data}, {Name: "two.html", Data: data}})
	if err != nil {
		t.Fatal("exact combined text bound", err)
	}
	if _, decoded := attachments.Decode(attachments.Encode("Read normally", files)); len(decoded) != 2 {
		t.Fatal("HTML expansion broke prepared frame")
	}
	if _, err = prepareChatInputs(context.Background(), []attachments.File{{Name: "one", Data: data}, {Name: "two", Data: data}, {Name: "three", Data: []byte("x")}}); err == nil {
		t.Fatal("combined extraction bound bypassed")
	}
}

func TestUploadOversizedDimensionsRejectBeforeDecode(t *testing.T) {
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	raw := b.Bytes()
	binary.BigEndian.PutUint32(raw[16:20], 5000)
	binary.BigEndian.PutUint32(raw[20:24], 5000)
	binary.BigEndian.PutUint32(raw[29:33], crc32.ChecksumIEEE(raw[12:29]))
	if _, err := chatInputImage(raw); err == nil {
		t.Fatal("dimension limit bypassed")
	}
	for _, bad := range [][]byte{raw[:33], []byte(strings.Repeat("not an image", 32)), {255, 216, 255}} {
		if _, err := chatInputImage(bad); err == nil {
			t.Fatal("corrupt image accepted")
		}
	}
}
