package documenttext

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestDOCXTextAndMalformedBounds(t *testing.T) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, _ := z.Create("word/document.xml")
	fmt.Fprint(w, `<w:document xmlns:w="urn:word"><w:body><w:p><w:r><w:t>Revenue 42</w:t></w:r></w:p></w:body></w:document>`)
	z.Close()
	text, e := DOCX(b.Bytes())
	if e != nil || !strings.Contains(text, "Revenue 42") {
		t.Fatal(text, e)
	}
	if _, e = DOCX([]byte("not a docx")); e == nil {
		t.Fatal("malformed document accepted")
	}
	b.Reset()
	z = zip.NewWriter(&b)
	w, _ = z.Create("word/document.xml")
	fmt.Fprint(w, "<doc>"+strings.Repeat("x", (1<<20)+1)+"</doc>")
	z.Close()
	if _, e = DOCX(b.Bytes()); e == nil {
		t.Fatal("expanded archive accepted")
	}
}
func TestPDFTextAndRejection(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"}
	stream := "BT /F1 12 Tf 72 700 Td (Revenue 42) Tj ET"
	objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	for i, obj := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	start := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), start)
	text, e := PDF(b.Bytes())
	if e != nil || !strings.Contains(text, "Revenue 42") {
		t.Fatal(text, e)
	}
	if _, e = PDF([]byte("not a pdf")); e == nil {
		t.Fatal("malformed PDF accepted")
	}
}
