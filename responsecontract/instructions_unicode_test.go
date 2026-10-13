package responsecontract

import (
	"reflect"
	"testing"
)

func TestInstructionSentencesUnicodeWhitespace(t *testing.T) {
	for _, space := range []string{"\u00a0", "\u2003", "\u202f", "\u3000"} {
		got := instructionSentences("First." + space + "Second.")
		if !reflect.DeepEqual(got, []string{"First", "Second"}) {
			t.Fatalf("space %q: %q", space, got)
		}
	}
}
