package responsecontract

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxLiteralBytes = 256
const maxLiteralCandidates = 8

var literalDirective = regexp.MustCompile(`(?im)(?:^|[.!?;][ \t]+)((?:(?:reply|respond)[ \t]+with|return|output|emit)[ \t]+exactly(?:[ \t]+(?:this|the following)[ \t]+(?:token|text|literal|string)(?:[ \t]+and[ \t]+nothing[ \t]+else)?)?:[ \t]*)([^\r\n]*)\r?$`)
var literalCondition = regexp.MustCompile(`(?i)\b(?:if|unless|otherwise)\b`)
var literalFollowOn = regexp.MustCompile(`(?i)(?:[.!?;,][ \t]+|[ \t]+and[ \t]+)(?:also[ \t]+|then[ \t]+)?(?:explain|include|add|write|return|reply|respond|output|emit|show|do not|don't|instead|ignore|disregard)\b`)

// inferLiteral recognizes a narrow, explicit literal directive without using
// the sentence splitter's transformed text as the target. Its target is the
// raw remainder of one physical line, with boundary whitespace ignored.
// Quotes, placeholders, conditional wording and follow-on instructions are
// deliberately left to the model. The target never enters diagnostic text.
func inferLiteral(instructions string) (string, bool) {
	matches := literalDirective.FindAllStringSubmatchIndex(instructions, maxLiteralCandidates+1)
	if len(matches) > maxLiteralCandidates {
		return "", true
	}
	var literal string
	for _, match := range matches {
		prefixStart, payloadStart, payloadEnd := match[2], match[4], match[5]
		// Replace only the prospective payload while checking whether its
		// directive is visible as current-user instructions. This reuses the
		// quote/fence/reference rules without losing punctuation in the actual
		// literal. The probe is an internal parser token, never a required reply.
		const probe = "__literal_response_visibility_probe__"
		visible := instructionSentences(instructions[:payloadStart] + probe + instructions[payloadEnd:])
		expected := strings.TrimSpace(instructions[prefixStart:payloadStart] + probe)
		index := -1
		for i, sentence := range visible {
			if sentence == expected {
				index = i
			}
		}
		if index < 0 {
			continue
		}
		value := strings.TrimSpace(instructions[payloadStart:payloadEnd])
		if index != len(visible)-1 || literal != "" || !validLiteral(value) {
			return "", true
		}
		literal = value
	}
	return literal, false
}

func validLiteral(value string) bool {
	if value == "" || len(value) > maxLiteralBytes || !utf8.ValidString(value) ||
		strings.ContainsAny(value, "`\"“”‘’<>") || strings.HasPrefix(value, "'") || strings.HasSuffix(value, "'") ||
		literalCondition.MatchString(value) || literalFollowOn.MatchString(value) {
		return false
	}
	for _, char := range value {
		if !unicode.IsPrint(char) {
			return false
		}
	}
	return true
}
