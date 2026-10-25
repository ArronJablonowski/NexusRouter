package webui

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"
)

func (p ApprovalProposal) validateFileCreate() error {
	if p.Version != 1 || len(p.Arguments) == 0 || len(p.Arguments) > MaxApprovalProposalBytes || !utf8.Valid(p.Arguments) || RejectDuplicateJSONFields(p.Arguments) != nil {
		return ErrContract
	}
	var a struct {
		Path    *string `json:"path"`
		Content *string `json:"content"`
	}
	d := json.NewDecoder(bytes.NewReader(p.Arguments))
	d.DisallowUnknownFields()
	if d.Decode(&a) != nil || d.Decode(new(any)) != io.EOF || a.Path == nil || a.Content == nil || len(*a.Content) > 64<<10 || len(*a.Path) > 4096 || len(*a.Path) == 0 || strings.ContainsAny(*a.Path, "\\") {
		return ErrContract
	}
	for _, part := range strings.Split(*a.Path, "/") {
		if part == "" || part == "." || part == ".." {
			return ErrContract
		}
		for _, r := range part {
			if r < 32 || r == 127 {
				return ErrContract
			}
		}
	}
	copy := p
	copy.Arguments = nil
	copy.Kind = ""
	copy.Version = 0
	b, _ := json.Marshal(copy)
	if string(b) != "{\"version\":0,\"kind\":\"\"}" {
		return ErrContract
	}
	return encodedWithin(p, MaxApprovalProposalBytes)
}
