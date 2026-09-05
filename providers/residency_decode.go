package providers

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

func decodeResidency(body []byte) ([]ResidentModel, error) {
	if !residencyJSON(body) {
		return nil, ErrResidency
	}
	var envelope struct {
		Models []json.RawMessage `json:"models"`
	}
	if strictResidency(body, &envelope) != nil || envelope.Models == nil || len(envelope.Models) > 256 {
		return nil, ErrResidency
	}
	result := make([]ResidentModel, 0, len(envelope.Models))
	seen := map[string]bool{}
	for _, raw := range envelope.Models {
		var item struct {
			Name          string    `json:"name"`
			Model         string    `json:"model"`
			Size          *uint64   `json:"size"`
			SizeVRAM      *uint64   `json:"size_vram"`
			Digest        string    `json:"digest"`
			ExpiresAt     time.Time `json:"expires_at"`
			ContextLength *int      `json:"context_length"`
			Details       *struct {
				ParentModel       string   `json:"parent_model"`
				Format            string   `json:"format"`
				Family            string   `json:"family"`
				Families          []string `json:"families"`
				ParameterSize     string   `json:"parameter_size"`
				QuantizationLevel string   `json:"quantization_level"`
			} `json:"details"`
		}
		if strictResidency(raw, &item) != nil || item.Size == nil || item.SizeVRAM == nil || *item.Size == 0 || *item.SizeVRAM > *item.Size || item.ExpiresAt.IsZero() || item.ExpiresAt.Year() < 1970 || len(item.Digest) != 64 {
			return nil, ErrResidency
		}
		for _, ch := range item.Digest {
			if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
				return nil, ErrResidency
			}
		}
		name, err := OllamaModelIdentity(item.Name)
		model, modelErr := OllamaModelIdentity(item.Model)
		if err != nil || modelErr != nil || name != model || seen[name] {
			return nil, ErrResidency
		}
		seen[name] = true
		contextLength := 0
		if item.ContextLength != nil {
			contextLength = *item.ContextLength
			if contextLength < 0 {
				return nil, ErrResidency
			}
		}
		result = append(result, ResidentModel{Name: item.Name, Model: item.Model, Size: *item.Size, SizeVRAM: *item.SizeVRAM, Digest: item.Digest, ExpiresAt: item.ExpiresAt, ContextLength: contextLength})
	}
	return result, nil
}

func decodeUnload(body []byte, identity string) error {
	if !residencyJSON(body) {
		return ErrResidency
	}
	var ack struct {
		Model      string    `json:"model"`
		CreatedAt  time.Time `json:"created_at"`
		Response   *string   `json:"response"`
		Done       *bool     `json:"done"`
		DoneReason string    `json:"done_reason"`
	}
	if strictResidency(body, &ack) != nil || ack.Done == nil || !*ack.Done || ack.DoneReason != "unload" || ack.Response == nil || *ack.Response != "" || ack.CreatedAt.IsZero() {
		return ErrResidency
	}
	actual, err := OllamaModelIdentity(ack.Model)
	if err != nil || actual != identity {
		return ErrResidency
	}
	return nil
}

func strictResidency(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return ErrResidency
	}
	return nil
}

// Recursively reject duplicate/case-aliased keys before typed decoding; JSON
// syntax, UTF-8 and surrogate pairing are checked before token normalization.
func residencyJSON(body []byte) bool {
	if len(body) > residencyLimit || !utf8.Valid(body) || !json.Valid(body) || !residencySurrogates(body) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	var visit func(int) bool
	visit = func(depth int) bool {
		token, err := d.Token()
		if err != nil {
			return false
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return true
		}
		if depth >= 16 {
			return false
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return false
				}
				s, ok := key.(string)
				if !ok || s != strings.ToLower(s) || seen[s] {
					return false
				}
				seen[s] = true
				if !visit(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for d.More() {
				if !visit(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim(']')
		}
		return false
	}
	if !visit(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

func residencySurrogates(body []byte) bool {
	quad := func(b []byte) uint16 {
		var v uint16
		for _, c := range b {
			v <<= 4
			switch {
			case c >= '0' && c <= '9':
				v += uint16(c - '0')
			case c >= 'a' && c <= 'f':
				v += uint16(c-'a') + 10
			case c >= 'A' && c <= 'F':
				v += uint16(c-'A') + 10
			}
		}
		return v
	}
	in := false
	for i := 0; i < len(body); i++ {
		if body[i] == '"' {
			in = !in
			continue
		}
		if !in || body[i] != '\\' {
			continue
		}
		if body[i+1] != 'u' {
			i++
			continue
		}
		v := quad(body[i+2 : i+6])
		if v >= 0xdc00 && v <= 0xdfff {
			return false
		}
		if v >= 0xd800 && v <= 0xdbff {
			if i+12 > len(body) || body[i+6] != '\\' || body[i+7] != 'u' {
				return false
			}
			low := quad(body[i+8 : i+12])
			if low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 11
		} else {
			i += 5
		}
	}
	return true
}
