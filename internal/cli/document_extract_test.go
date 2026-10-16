package cli

import (
	"bytes"
	"github.com/ArronJablonowski/NexusRouter/attachments"
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

func TestDocumentExtractOversizedBytesHaveNoOutput(t *testing.T) {
	var out bytes.Buffer
	if runDocumentExtract([]string{"pdf"}, strings.NewReader(strings.Repeat("x", attachments.MaxFileBytes+1)), &out) == 0 || out.Len() != 0 {
		t.Fatal("oversized reader input accepted")
	}
}
