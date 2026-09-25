package responsecontract

import (
	"regexp"
	"strings"
	"unicode"
)

var referenceLabel = regexp.MustCompile(`(?i)^(?:#{1,6}\s*)?(?:retrieved(?:\s+(?:context|document|web page|text))?|reference(?:\s+(?:text|document))?|quoted(?:\s+(?:text|document))?|tool(?:\s+(?:output|result))?|context|document|source|example|transcript|log)(?:\s+says)?\s*:`)

func instructionSentences(input string) []string {
	input = maskReferenceTags(input)
	var visible strings.Builder
	fence := ""
	inReference := false
	for _, line := range strings.Split(input, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			if fence == "" {
				fence = marker
			} else if fence == marker {
				fence = ""
			}
			visible.WriteByte('\n')
			continue
		}
		if trimmed == "" {
			inReference = false
		}
		if referenceLabel.MatchString(trimmed) {
			inReference = true
		}
		if fence != "" || inReference || strings.HasPrefix(trimmed, ">") || strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") {
			visible.WriteByte('\n')
			continue
		}
		visible.WriteString(line)
		visible.WriteByte('\n')
	}
	input = maskQuotes(visible.String())
	var sentences []string
	start := 0
	for index, r := range input {
		if r == '\n' || (r == '.' || r == '!' || r == '?' || r == ';') && (index+1 == len(input) || unicode.IsSpace(rune(input[index+1]))) {
			if sentence := strings.TrimSpace(input[start:index]); sentence != "" {
				sentences = append(sentences, sentence)
			}
			start = index + 1
		}
	}
	if sentence := strings.TrimSpace(input[start:]); sentence != "" {
		sentences = append(sentences, sentence)
	}
	return sentences
}

func maskReferenceTags(input string) string {
	for _, tag := range []string{"untrusted_text", "context", "reference", "document", "tool_result", "tool_output", "example", "quote"} {
		// An unclosed reference tag conservatively hides the rest of the input.
		pattern := regexp.MustCompile(`(?is)<` + tag + `(?:\s[^>]*)?>.*?(?:</` + tag + `\s*>|$)`)
		input = pattern.ReplaceAllString(input, " ")
	}
	return input
}

func maskQuotes(input string) string {
	chars := []rune(input)
	var quote rune
	escaped := false
	for index, char := range chars {
		if quote != 0 {
			if escaped {
				escaped = false
			} else if char == '\\' {
				escaped = true
			} else if char == quote {
				quote = 0
			}
			if char != '\n' {
				chars[index] = ' '
			}
			continue
		}
		if char == '"' || char == '`' || char == '“' || char == '‘' || char == '\'' && (index == 0 || !unicode.IsLetter(chars[index-1])) {
			quote = char
			if char == '“' {
				quote = '”'
			} else if char == '‘' {
				quote = '’'
			}
			chars[index] = ' '
		}
	}
	return string(chars)
}
