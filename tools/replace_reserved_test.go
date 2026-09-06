package tools

import "testing"

func TestExtensionCannotOverrideReplaceFileBuiltin(t *testing.T) {
	if _, err := NewExtension([]Definition{extensionDefinition("replace_file")}, &Policy{Default: Allow}); err == nil {
		t.Fatal("replace builtin authority overridden")
	}
}
