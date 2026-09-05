package api

import (
	"encoding/json"
	"unicode/utf8"
)

// memoryJSONUnicodeValid rejects JSON strings that encoding/json would decode
// lossily by replacing unpaired UTF-16 surrogate escapes with U+FFFD. Literal
// replacement characters and correctly paired surrogate escapes remain valid.
// This is an encoding boundary, not a substitute for command/schema validation.
func memoryJSONUnicodeValid(body []byte) bool {
	if len(body) > memoryBodyLimit || !utf8.Valid(body) || !json.Valid(body) {
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
		// json.Valid guarantees a complete legal escape. Skip escaped quotes
		// and backslashes so a literal "\\uD800" is never treated as UTF-16.
		if body[i+1] != 'u' {
			i++
			continue
		}
		value := memoryHexQuad(body[i+2 : i+6])
		if value >= 0xdc00 && value <= 0xdfff {
			return false
		}
		if value >= 0xd800 && value <= 0xdbff {
			if i+12 > len(body) || body[i+6] != '\\' || body[i+7] != 'u' {
				return false
			}
			low := memoryHexQuad(body[i+8 : i+12])
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

// Input is exactly four hexadecimal digits established by json.Valid.
func memoryHexQuad(digits []byte) uint16 {
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
