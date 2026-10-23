// Package documenttext extracts text without executing document macros, scripts,
// links or external relationships. PDF extraction is invoked in an owned child.
package documenttext

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"github.com/ledongthuc/pdf"
	"io"
	"strings"
)

const MaxText = 64 << 10

var ErrDocument = errors.New("document text unavailable")

func PDF(data []byte) (text string, err error) { return pdfText(data, nil) }

func pdfText(data []byte, ocr func(int) (string, error)) (text string, err error) {
	defer func() {
		if recover() != nil {
			text = ""
			err = ErrDocument
		}
	}()
	r, e := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if e != nil || r.NumPage() < 1 || r.NumPage() > 40 || !r.Trailer().Key("Encrypt").IsNull() {
		return "", ErrDocument
	}
	var b strings.Builder
	for n := 1; n <= r.NumPage(); n++ {
		page := r.Page(n)
		s, e := page.GetPlainText(nil)
		if ocr != nil && (e != nil || strings.TrimSpace(s) == "" || pageHasImages(page.Resources(), 0)) {
			native, nativeErr := s, e
			s, e = ocr(n)
			if e == nil && nativeErr == nil && strings.TrimSpace(native) != "" {
				if strings.TrimSpace(s) == "" {
					s = fmt.Sprintf("[Page %d: searchable text; OCR found no text in page imagery]\n%s", n, native)
				} else {
					s = fmt.Sprintf("[Page %d: searchable text]\n%s\n%s", n, native, s)
				}
			}
		}
		if e != nil {
			return "", e
		}
		if len(s)+b.Len()+1 > MaxText {
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
			if t.Name.Local == "br" || t.Name.Local == "cr" {
				b.WriteByte('\n')
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

// Form XObjects can contain scans too. Bound recursion on hostile/cyclic resources.
func pageHasImages(resources pdf.Value, depth int) bool {
	if depth > 8 {
		return true
	}
	objects := resources.Key("XObject")
	for _, key := range objects.Keys() {
		v := objects.Key(key)
		if v.Key("Subtype").Name() == "Image" {
			return true
		}
		if v.Key("Subtype").Name() == "Form" && pageHasImages(v.Key("Resources"), depth+1) {
			return true
		}
	}
	return false
}
