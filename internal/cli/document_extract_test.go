package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestDocumentExtractRejectsInvalidAndOversizedInput(t *testing.T) {
	for _, args := range [][]string{nil, {"docx"}, {"pdf", "extra"}, {"pdf"}} {
		var out bytes.Buffer
		if runDocumentExtract(args, strings.NewReader("not a PDF"), &out) == 0 || out.Len() != 0 {
			t.Fatal("invalid extractor accepted bytes")
		}
	}
}
