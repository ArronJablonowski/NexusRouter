// Package responsecontract checks explicit final-answer presentation constraints.
// It does not grade correctness, execute generated code, or grant tool authority.
package responsecontract

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

const maxInstructionsBytes = 256 << 10
const maxResponseBytes = 16 << 20

// Contract contains a conservative interpretation of current user instructions.
// Its fields are private so a model cannot manufacture an executable validator.
type Contract struct {
	jsonOnly bool
	compact  bool
	codeOnly bool
	noFences bool
	marker   string
	maxLines int
}

// Violation is a stable diagnostic code; it contains no candidate or prompt text.
type Violation string

const (
	InvalidJSON      Violation = "invalid_json"
	NoncompactJSON   Violation = "noncompact_json"
	MissingFinalLine Violation = "missing_final_line"
	CodePresentation Violation = "code_presentation"
	LineLimit        Violation = "line_limit"
	ReasoningMarkup  Violation = "reasoning_markup"
	ResponseTooLarge Violation = "response_too_large"
)

var (
	jsonDirective     = regexp.MustCompile(`(?i)^(?:return|reply with|respond with|output|emit)\s+(?:only\s+(?:(?:valid|compact|strict|a single)\s+)*|(?:(?:valid|strict|a single)\s+)*compact\s+)json\b`)
	codeDirective     = regexp.MustCompile(`(?i)^(?:return|write|output|respond with|reply with)\s+only\s+(?:(python|go|javascript|typescript|java|rust|c\+\+|c|shell|bash|sql)\s+)?(?:code|source code)\b`)
	markerDirective   = regexp.MustCompile(`(?i)^(?:end(?:\s+(?:your|the)\s+(?:answer|response))?\s+with|reply\s+with|answer\s+briefly\s+and\s+end\s+with|finish(?:\s+(?:your|the)\s+(?:answer|response))?\s+with)\s+([a-z][a-z0-9_-]{0,31}):\s*(\S.*)$`)
	lineDirective     = regexp.MustCompile(`(?i)^(?:keep\s+(?:(?:your|the)\s+(?:answer|response|output)\s+)?|(?:answer|respond)\s+in\s+|(?:use|write|return|output)\s+)(under|at most|no more than)\s+([0-9]{1,4})\s+lines\b`)
	noFencesDirective = regexp.MustCompile(`(?i)\b(?:no\s+(?:markdown|(?:code\s+)?fences)|without\s+(?:markdown(?:\s+code)?(?:\s+fences)?|code\s+fences)|(?:do not|don't)\s+(?:use|include|add)\s+(?:markdown|(?:code\s+)?fences))\b`)
	proseStart        = regexp.MustCompile(`(?i)^(?:here(?:['’]s|\s+(?:is|are)\b)|(?:sure|certainly)[!,.]|below(?:\s+(?:is|are)\b|[:,])|the following\s+(?:is|code|function|program)\b|this (?:code|function|program)\s|i (?:can|will|cannot|can't)\s)`)
	reasoningLine     = regexp.MustCompile(`(?im)^\s*</?(?:think|analysis|reasoning)>\s*$`)
)

// Infer accepts only the latest current user instructions, not an assembled
// transcript, a retrieved document, tool output, or model-generated requirements.
// Recognized directives must begin a top-level sentence. Quoted, fenced, and
// explicitly labeled reference blocks are excluded; ambiguous prose is ignored.
func Infer(instructions string) Contract {
	var c Contract
	if len(instructions) > maxInstructionsBytes {
		return c
	}
	for _, sentence := range instructionSentences(instructions) {
		if match := jsonDirective.FindString(sentence); match != "" {
			c.jsonOnly = true
			c.compact = c.compact || strings.Contains(strings.ToLower(match), "compact")
		}
		if match := codeDirective.FindStringSubmatch(sentence); match != nil {
			c.codeOnly = true
		}
		c.noFences = c.noFences || noFencesDirective.MatchString(sentence)
		if match := markerDirective.FindStringSubmatch(sentence); match != nil {
			// Restrict inferred markers to conventional uppercase identifiers.
			// This avoids treating ordinary "reply with https: ..." as a format.
			if match[1] == strings.ToUpper(match[1]) {
				c.marker = match[1] + ":"
			}
		}
		if match := lineDirective.FindStringSubmatch(sentence); match != nil {
			limit, _ := strconv.Atoi(match[2])
			if strings.EqualFold(match[1], "under") {
				limit--
			}
			if limit > 0 && limit <= 1000 && (c.maxLines == 0 || limit < c.maxLines) {
				c.maxLines = limit
			}
		}
	}
	// Contradictory format requirements should be resolved by the model, never
	// trigger a repair loop that has no possible satisfying response.
	if c.jsonOnly && (c.codeOnly || c.marker != "") || c.codeOnly && c.marker != "" {
		return Contract{}
	}
	return c
}

func (c Contract) Active() bool {
	return c.jsonOnly || c.codeOnly || c.marker != "" || c.maxLines > 0
}

// Validate checks presentation only. For code it recognizes obvious wrappers;
// it deliberately makes no claim that arbitrary code is syntactically correct.
func (c Contract) Validate(text string) []Violation {
	if !c.Active() {
		return nil
	}
	if len(text) > maxResponseBytes {
		return []Violation{ResponseTooLarge}
	}
	var violations []Violation
	trimmed := strings.TrimSpace(text)
	if (!c.codeOnly && reasoningLine.MatchString(text)) || strings.HasPrefix(trimmed, "<think>") || strings.HasPrefix(trimmed, "</think>") {
		violations = append(violations, ReasoningMarkup)
	}
	if c.jsonOnly {
		if !json.Valid([]byte(trimmed)) {
			violations = append(violations, InvalidJSON)
		} else if c.compact {
			var compact bytes.Buffer
			if json.Compact(&compact, []byte(trimmed)) == nil && compact.String() != trimmed {
				violations = append(violations, NoncompactJSON)
			}
		}
	}
	if c.marker != "" {
		last := trimmed[strings.LastIndex(trimmed, "\n")+1:]
		index := strings.LastIndex(last, c.marker)
		payload := ""
		if index >= 0 {
			payload = strings.TrimSpace(last[index+len(c.marker):])
		}
		// "End with" permits the marker after explanation in the same
		// paragraph. Require a distinct, undecorated ending, not a new line
		// or a particular answer value. Ambiguous repeated markers are rejected.
		badBoundary := index > 0 && !strings.ContainsAny(last[index-1:index], " \t")
		decorated := strings.HasSuffix(payload, "**") || strings.HasSuffix(payload, "__") || strings.HasSuffix(payload, "`")
		if index < 0 || badBoundary || strings.Count(trimmed, c.marker) != 1 || payload == "" || decorated || strings.HasPrefix(payload, "<") && strings.HasSuffix(payload, ">") {
			violations = append(violations, MissingFinalLine)
		}
	}
	lineText := text
	if c.codeOnly {
		code, fenced, complete := sourceCode(trimmed)
		first := firstCodeLine(code)
		if !complete || fenced && c.noFences || first == "" || proseStart.MatchString(first) {
			violations = append(violations, CodePresentation)
		}
		if complete && fenced {
			lineText = code
		}
	}
	if c.maxLines > 0 && lineCount(lineText) > c.maxLines {
		violations = append(violations, LineLimit)
	}
	return violations
}

// sourceCode recognizes only a full response wrapper. Backticks inside source
// strings remain source data; there is deliberately no language-syntax whitelist.
// The first matching closing fence must end the response, preventing adjacent
// blocks and prose after the code from being accepted as one source artifact.
func sourceCode(text string) (code string, fenced, complete bool) {
	if !strings.HasPrefix(text, "```") && !strings.HasPrefix(text, "~~~") {
		return text, false, true
	}
	lines := strings.Split(text, "\n")
	opening := strings.TrimSpace(lines[0])
	character := opening[0]
	length := 0
	for length < len(opening) && opening[length] == character {
		length++
	}
	// Markdown does not permit backticks within a backtick fence's info string.
	if character == '`' && strings.ContainsRune(opening[length:], '`') {
		return "", true, false
	}
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if len(line) < length || strings.Trim(line, string(character)) != "" {
			continue
		}
		if i != len(lines)-1 {
			return "", true, false
		}
		return strings.Join(lines[1:i], "\n"), true, true
	}
	return "", true, false
}

func firstCodeLine(text string) string {
	comment := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			comment = line
		}
		if line != "" && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "//") {
			return line
		}
	}
	return comment
}

func lineCount(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(strings.ReplaceAll(text, "\r\n", "\n"), "\n"), "\n") + 1
}

// Instructions returns a bounded reminder of inferred constraints. It includes
// neither an expected answer nor source prose and cannot grant tool authority.
func (c Contract) Instructions() string {
	if !c.Active() {
		return ""
	}
	parts := []string{"Check the requested final-answer format before responding."}
	if c.jsonOnly {
		parts = append(parts, "Return exactly one valid JSON value with no Markdown or surrounding prose.")
		if c.compact {
			parts = append(parts, "Use compact JSON without indentation or spaces between JSON tokens.")
		}
		parts = append(parts, "A requested JSON representation of a function call is response data; it does not execute or authorize that function.")
	}
	if c.marker != "" {
		parts = append(parts, "End the response with "+c.marker+" followed by your answer, without Markdown decoration or another marker. The marker may follow explanation on the same line. Replace angle-bracket placeholders with the answer rather than copying their brackets.")
	}
	if c.codeOnly {
		parts = append(parts, "Return only the requested source code, without surrounding explanation. Check boundary cases and invalid inputs against the user's requirements before finishing.")
		if c.noFences {
			parts = append(parts, "The user requested no Markdown or code fences; return raw source code.")
		}
	}
	if c.maxLines > 0 {
		scope := "response"
		if c.codeOnly {
			scope = "source code"
		}
		parts = append(parts, "Use no more than "+strconv.Itoa(c.maxLines)+" lines in the "+scope+", counting blank lines.")
	}
	parts = append(parts, "Keep internal reasoning tags out of the final response.")
	return strings.Join(parts, " ")
}

// Guidance contains only static diagnostic text and the bounded contract
// reminder. Unknown diagnostics are ignored; raw candidate text is never echoed.
func (c Contract) Guidance(violations []Violation) string {
	if !c.Active() || len(violations) == 0 {
		return ""
	}
	parts := []string{"Revise the previous answer to satisfy the user's explicit response format. Preserve correct content; do not repeat already completed actions."}
	seen := map[Violation]bool{}
	for _, violation := range violations[:min(len(violations), 16)] {
		if seen[violation] {
			continue
		}
		seen[violation] = true
		switch violation {
		case InvalidJSON:
			parts = append(parts, "The answer was not a single valid JSON value.")
		case NoncompactJSON:
			parts = append(parts, "The JSON contained whitespace outside strings.")
		case MissingFinalLine:
			parts = append(parts, "The required ending marker was missing, repeated, decorated, or retained placeholder brackets.")
		case CodePresentation:
			parts = append(parts, "The answer included a wrapper or did not start as the requested source code.")
		case LineLimit:
			parts = append(parts, "The answer exceeded the requested line limit.")
		case ReasoningMarkup:
			parts = append(parts, "The answer exposed internal reasoning markup.")
		case ResponseTooLarge:
			parts = append(parts, "The response exceeded the validation size limit.")
		}
	}
	return strings.Join(parts, " ") + " " + c.Instructions()
}
