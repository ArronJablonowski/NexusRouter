package documenttext

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func mixedPDF() []byte {
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 6 0 R] /Count 2 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		"<< /Length 43 >>\nstream\nBT /F1 12 Tf 72 700 Td (Native 74319) Tj ET\nendstream",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> /XObject << /Scan 8 0 R >> >> /Contents 7 0 R >>",
		"<< /Length 42 >>\nstream\nBT /F1 12 Tf 72 700 Td (Header only) Tj ET\nendstream",
		"<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /Length 1 >>\nstream\nX\nendstream",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, obj := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	start := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, n := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", n)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), start)
	return b.Bytes()
}

func TestPDFMixedNativeAndImagePages(t *testing.T) {
	var pages []int
	s, err := pdfText(mixedPDF(), func(n int) (string, error) { pages = append(pages, n); return "Scan 68423", nil })
	if err != nil || !strings.Contains(s, "Native 74319") || !strings.Contains(s, "Scan 68423") || !strings.Contains(s, "Header only") || len(pages) != 1 || pages[0] != 2 {
		t.Fatalf("text=%q pages=%v err=%v", s, pages, err)
	}
	for _, callback := range []func(int) (string, error){
		func(int) (string, error) { return "", ErrDependency },
		func(int) (string, error) { return strings.Repeat("x", MaxText), nil },
	} {
		if s, err = pdfText(mixedPDF(), callback); err == nil || s != "" {
			t.Fatal("partial or over-limit document accepted")
		}
	}
	_, err = pdfText(mixedPDF(), func(int) (string, error) { return "", ErrDependency })
	if !errors.Is(err, ErrDependency) {
		t.Fatal("missing reader was hidden", err)
	}
}

func TestLocalReadersFailClosed(t *testing.T) {
	for _, data := range [][]byte{[]byte("plain text renamed .doc"), bytes.Repeat([]byte{0}, 512), append([]byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}, make([]byte, 504)...)} {
		if text, err := Local(context.Background(), "doc", data); err == nil || text != "" {
			t.Fatal("invalid DOC accepted")
		}
	}
	if _, err := localTool("nexus-nonexistent-document-reader"); !errors.Is(err, ErrDependency) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runLocal(ctx, "textutil", nil, MaxText, "-help"); err == nil {
		t.Fatal("canceled reader ran")
	}
	var out boundedOutput
	out.limit = 3
	if _, err := out.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write([]byte("d")); err == nil || string(out.Bytes()) != "abc" {
		t.Fatal("output limit bypassed")
	}
}

func TestLegacyDOCWithNativeReader(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS native reader integration; Linux requires antiword")
	}
	doc, err := runLocal(context.Background(), "textutil", []byte("Legacy Word reference 68423\n"), MaxText, "-convert", "doc", "-format", "txt", "-stdin", "-stdout")
	if err != nil {
		t.Fatal(err)
	}
	text, err := Local(context.Background(), "doc", doc)
	if err != nil || !strings.Contains(text, "68423") {
		t.Fatal(text, err)
	}
}

func TestPDFNativeTextSurvivesEmptyOCR(t *testing.T) {
	text, err := pdfText(mixedPDF(), func(int) (string, error) { return "", nil })
	if err != nil || !strings.Contains(text, "Header only") {
		t.Fatal("OCR discarded searchable text", err)
	}
}
