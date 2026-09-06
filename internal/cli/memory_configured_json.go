package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"

	"github.com/ArronJablonowski/DarwinRouter/memory"
)

const configuredMemoryBodyLimit = 128 << 10

func decodeConfiguredMemoryFact(input io.Reader) (memory.Fact, error) {
	zero := memory.Fact{}
	if input == nil {
		return zero, memory.ErrInput
	}
	body, err := io.ReadAll(io.LimitReader(input, configuredMemoryBodyLimit+1))
	if err != nil || !configuredMemoryUnicodeValid(body) {
		return zero, memory.ErrInput
	}
	allowed := map[string]bool{}
	for _, name := range []string{"version", "id", "scope", "revision", "content", "provenance", "confidence", "privacy", "created", "updated", "last_use", "expires"} {
		allowed[name] = true
	}
	seen := map[string]bool{}
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return zero, memory.ErrInput
	}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || !allowed[key] || seen[key] {
			return zero, memory.ErrInput
		}
		seen[key] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return zero, memory.ErrInput
		}
	}
	if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
		return zero, memory.ErrInput
	}
	if _, err := decoder.Token(); err != io.EOF {
		return zero, memory.ErrInput
	}
	for _, name := range []string{"version", "id", "scope", "revision", "content", "provenance", "confidence", "privacy", "created", "updated"} {
		if !seen[name] {
			return zero, memory.ErrInput
		}
	}
	var fact memory.Fact
	if json.Unmarshal(body, &fact) != nil || fact.Validate() != nil {
		return zero, memory.ErrInput
	}
	return fact, nil
}

// encoding/json replaces unpaired UTF-16 escapes; reject instead of silently
// changing factual content, provenance or identity. Literal U+FFFD is valid.
func configuredMemoryUnicodeValid(body []byte) bool {
	if len(body) > configuredMemoryBodyLimit || !utf8.Valid(body) || !json.Valid(body) {
		return false
	}
	inString := false
	for i := 0; i < len(body); i++ {
		if body[i] == '"' {
			inString = !inString
			continue
		}
		if !inString || body[i] != '\\' {
			continue
		}
		if body[i+1] != 'u' {
			i++
			continue
		}
		value := configuredMemoryHexQuad(body[i+2 : i+6])
		if value >= 0xdc00 && value <= 0xdfff {
			return false
		}
		if value >= 0xd800 && value <= 0xdbff {
			if i+12 > len(body) || body[i+6] != '\\' || body[i+7] != 'u' {
				return false
			}
			low := configuredMemoryHexQuad(body[i+8 : i+12])
			if low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 11
			continue
		}
		i += 5
	}
	return true
}

func configuredMemoryHexQuad(digits []byte) uint16 {
	var value uint16
	for _, digit := range digits {
		value <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			value += uint16(digit - '0')
		case digit >= 'a' && digit <= 'f':
			value += uint16(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			value += uint16(digit-'A') + 10
		}
	}
	return value
}
