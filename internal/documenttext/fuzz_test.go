package documenttext

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func FuzzDOCXBoundedExtraction(f *testing.F) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, _ := z.Create("word/document.xml")
	w.Write([]byte(`<document><p><t>valid text</t></p></document>`))
	z.Close()
	f.Add(b.Bytes())
	f.Add([]byte("not a zip"))
	f.Add([]byte("PK\x03\x04"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			return
		}
		text, err := DOCX(data)
		if err == nil && (len(text) > MaxText || strings.TrimSpace(text) == "") {
			t.Fatal("invalid extraction success")
		}
	})
}
