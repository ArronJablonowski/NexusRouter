package attachments

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestUploadFileAndDecodedBoundsMatrix(t *testing.T) {
	for _, name := range []string{".", "..", "../x", "a/b", "a\\b", "a\x00b", "a\nb", "a\u0085b", strings.Repeat("é", 101)} {
		if Validate([]File{{Name: name, Data: []byte("x")}}) == nil {
			t.Fatal("unsafe name accepted")
		}
	}
	if Validate([]File{{Name: strings.Repeat("é", 100), Data: bytes.Repeat([]byte("x"), MaxTotalBytes)}}) != nil {
		t.Fatal("exact UTF-8/byte boundary rejected")
	}
	if Validate([]File{{Name: "a", Data: bytes.Repeat([]byte("x"), MaxTotalBytes)}, {Name: "b", Data: []byte("x")}}) == nil {
		t.Fatal("combined limit bypassed")
	}
	for _, inputs := range [][]Input{
		{{Name: "a", Text: strings.Repeat("x", MaxTextBytes+1)}},
		{{Name: "a", Text: strings.Repeat("x", MaxTextBytes)}, {Name: "b", Text: strings.Repeat("x", MaxTextBytes)}, {Name: "c", Text: "x"}},
	} {
		if _, files := Decode(Encode("read", inputs)); files != nil {
			t.Fatal("oversized decoded content admitted")
		}
	}
}

func FuzzUploadFrameRoundTrip(f *testing.F) {
	f.Add("plain text")
	f.Add(strings.Repeat("<", MaxTextBytes))
	f.Add("héllo\n\"\\<>&")
	f.Fuzz(func(t *testing.T, text string) {
		if !utf8.ValidString(text) || len(text) > MaxTextBytes {
			return
		}
		prompt, files := Decode(Encode("read", []Input{{Name: "file.txt", Text: text}}))
		if prompt != "read" || len(files) != 1 || files[0].Text != text {
			t.Fatal("frame lost valid text")
		}
	})
}
