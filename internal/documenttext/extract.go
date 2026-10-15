// Package documenttext extracts text without executing document macros, scripts,
// links or external relationships. PDF extraction is invoked in an owned child.
package documenttext

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"github.com/ledongthuc/pdf"
	"io"
	"strings"
)

const MaxText = 64 << 10

var ErrDocument = errors.New("document text unavailable")

func PDF(data []byte) (text string, err error) {
	defer func() {
		if recover() != nil {
			text = ""
			err = ErrDocument
		}
	}()
	r, e := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if e != nil || r.NumPage() < 1 || r.NumPage() > 40 {
		return "", ErrDocument
	}
	var b strings.Builder
	for n := 1; n <= r.NumPage(); n++ {
		page := r.Page(n)
		s, e := page.GetPlainText(nil)
		if e != nil || len(s)+b.Len() > MaxText {
			return "", ErrDocument
		}
		b.WriteString(s)
		b.WriteByte('\n')
	}
	if strings.TrimSpace(b.String()) == "" {
		return "", ErrDocument
	}
	return b.String(), nil
}
func DOCX(data []byte) (string, error) {
	z, e := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if e != nil || len(z.File) > 256 {
		return "", ErrDocument
	}
	var doc *zip.File
	for _, f := range z.File {
		if f.Name == "word/document.xml" {
			if doc != nil || f.UncompressedSize64 > 1<<20 {
				return "", ErrDocument
			}
			doc = f
		}
	}
	if doc == nil {
		return "", ErrDocument
	}
	r, e := doc.Open()
	if e != nil {
		return "", ErrDocument
	}
	defer r.Close()
	dec := xml.NewDecoder(io.LimitReader(r, (1<<20)+1))
	var b strings.Builder
	inText := false
	for {
		tok, e := dec.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", ErrDocument
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" {
				inText = true
			}
			if t.Name.Local == "tab" {
				b.WriteByte('\t')
			}
		case xml.EndElement:
			if t.Name.Local == "t" {
				inText = false
			}
			if t.Name.Local == "p" {
				b.WriteByte('\n')
			}
		case xml.CharData:
			if inText {
				b.Write(t)
			}
		}
		if b.Len() > MaxText {
			return "", ErrDocument
		}
	}
	if strings.TrimSpace(b.String()) == "" {
		return "", ErrDocument
	}
	return b.String(), nil
}
