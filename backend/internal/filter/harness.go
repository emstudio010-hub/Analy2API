package filter

import (
	"regexp"
	"strings"
)

type harnessSpan struct {
	Start           int
	End             int
	DropLineIfEmpty bool
}

var harnessXMLTags = []string{
	"system-reminder",
	"permissions instructions",
	"environment_context",
	"collaboration_mode",
	"skills_instructions",
	"plugins_instructions",
	"user_instructions",
	"INSTRUCTIONS",
}

var harnessHeaderLineRe = regexp.MustCompile(
	`(?i)(?:x-anthropic-billing-header:|^[ \t]*x-anthropic-billing-header\b|\bcc_version=|\bcc_entrypoint=|\bcch=[a-z0-9_-]+)`,
)

var (
	agentsMDHeadingRe = regexp.MustCompile(`(?i)^[ \t]*# AGENTS\.md instructions(?:\b|$)`)
	claudeMDHeadingRe = regexp.MustCompile(`(?i)^[ \t]*# claudeMd(?:\b|$)`)
)

// HasHarnessInjection reports whether text contains any client-harness-injected
// blocks or headers outside user code fences and inline backticks.
func HasHarnessInjection(text string) bool {
	return len(findHarnessSpans(text)) > 0
}

// applyToHarnessSpans applies fn exclusively to detected harness-injected spans,
// leaving natural user questions and pasted source code byte-for-byte untouched.
func applyToHarnessSpans(text string, fn func(string) string) string {
	spans := findHarnessSpans(text)
	if len(spans) == 0 {
		return text
	}

	var b strings.Builder
	b.Grow(len(text))
	cursor := 0

	for i, sp := range spans {
		if sp.Start < cursor {
			continue
		}
		prefix := text[cursor:sp.Start]
		replaced := fn(text[sp.Start:sp.End])

		if sp.DropLineIfEmpty && strings.TrimSpace(replaced) == "" {
			nextCursor := sp.End
			// Consume the line ending right after the emptied header line when present.
			if nextCursor < len(text) {
				if text[nextCursor] == '\r' && nextCursor+1 < len(text) && text[nextCursor+1] == '\n' {
					nextCursor += 2
				} else if text[nextCursor] == '\n' {
					nextCursor++
				}
				// If this pruned header was at the very start of text, collapse an extra blank line immediately following it.
				if sp.Start == 0 {
					for nextCursor < len(text) && (text[nextCursor] == '\r' || text[nextCursor] == '\n') {
						if i+1 < len(spans) && nextCursor >= spans[i+1].Start {
							break
						}
						nextCursor++
					}
				}
				b.WriteString(prefix)
				cursor = nextCursor
				continue
			}

			// At EOF: trim a single preceding newline if this was on its own trailing line.
			prefix = trimOneTrailingNewline(prefix)
			b.WriteString(prefix)
			cursor = sp.End
			continue
		}

		b.WriteString(prefix)
		b.WriteString(replaced)
		cursor = sp.End
	}

	if cursor < len(text) {
		b.WriteString(text[cursor:])
	}
	return b.String()
}

func trimOneTrailingNewline(s string) string {
	if strings.HasSuffix(s, "\r\n") {
		return s[:len(s)-2]
	}
	if strings.HasSuffix(s, "\n") {
		return s[:len(s)-1]
	}
	return s
}

func findHarnessSpans(text string) []harnessSpan {
	if text == "" {
		return nil
	}

	var spans []harnessSpan
	cursor := 0
	n := len(text)

	for cursor < n {
		atLineStart := cursor == 0 || text[cursor-1] == '\n'

		// 1. Skip fenced markdown code blocks opened at top level (user-pasted code).
		if atLineStart {
			if endFence, ok := scanFencedCodeBlock(text, cursor); ok {
				cursor = endFence
				continue
			}
		}

		// 2. Skip inline backtick spans (user referencing tags/headers in prose).
		if text[cursor] == '`' {
			cursor = scanInlineBackticks(text, cursor)
			continue
		}

		// 3. Check known XML harness blocks (<system-reminder>, <environment_context>, etc.).
		if text[cursor] == '<' {
			if endXML, ok := matchXMLHarnessBlock(text, cursor, atLineStart); ok {
				spans = append(spans, harnessSpan{Start: cursor, End: endXML})
				cursor = endXML
				continue
			}
			cursor++
			continue
		}

		// 4. At line start, check markdown harness sections and billing-header/telemetry lines.
		if atLineStart {
			lineEnd := findLineEnd(text, cursor)
			line := text[cursor:lineEnd]

			if agentsMDHeadingRe.MatchString(line) {
				endSection := findAgentsMDSectionEnd(text, cursor, lineEnd)
				spans = append(spans, harnessSpan{Start: cursor, End: endSection})
				cursor = endSection
				continue
			}

			if claudeMDHeadingRe.MatchString(line) {
				endSection := findNextTopLevelHarnessXML(text, lineEnd)
				spans = append(spans, harnessSpan{Start: cursor, End: endSection})
				cursor = endSection
				continue
			}

			if isHarnessHeaderLine(line) {
				spans = append(spans, harnessSpan{
					Start:           cursor,
					End:             lineEnd,
					DropLineIfEmpty: true,
				})
				cursor = advancePastNewline(text, lineEnd)
				continue
			}
		}

		// Jump to the next candidate byte ('<', '`', or start of next line).
		next := nextInterestingPos(text, cursor)
		cursor = next
	}

	return spans
}

func nextInterestingPos(text string, from int) int {
	n := len(text)
	if from < n && text[from] == '\n' {
		if from+1 < n {
			return from + 1
		}
		return n
	}
	for i := from + 1; i < n; i++ {
		switch text[i] {
		case '<', '`':
			return i
		case '\n':
			if i+1 < n {
				return i + 1
			}
			return n
		}
	}
	return n
}

func findLineEnd(text string, start int) int {
	idx := strings.IndexByte(text[start:], '\n')
	if idx < 0 {
		return len(text)
	}
	end := start + idx
	if end > start && text[end-1] == '\r' {
		end--
	}
	return end
}

func advancePastNewline(text string, lineEnd int) int {
	if lineEnd < len(text) && text[lineEnd] == '\r' {
		lineEnd++
	}
	if lineEnd < len(text) && text[lineEnd] == '\n' {
		lineEnd++
	}
	return lineEnd
}

// scanFencedCodeBlock detects a top-level Markdown fenced code block (``` or ~~~)
// starting at lineStart and returns the byte offset immediately after its closing fence.
func scanFencedCodeBlock(text string, lineStart int) (int, bool) {
	n := len(text)
	pos := lineStart
	spaces := 0
	for pos < n && text[pos] == ' ' && spaces < 3 {
		pos++
		spaces++
	}
	if pos >= n || (text[pos] != '`' && text[pos] != '~') {
		return 0, false
	}

	fenceChar := text[pos]
	fenceLen := 0
	for pos < n && text[pos] == fenceChar {
		fenceLen++
		pos++
	}
	if fenceLen < 3 {
		return 0, false
	}

	lineEnd := findLineEnd(text, lineStart)
	if fenceChar == '`' {
		// CommonMark: backtick fence info string cannot contain backticks (else it's inline code).
		if strings.IndexByte(text[pos:lineEnd], '`') >= 0 {
			return 0, false
		}
	}

	scan := advancePastNewline(text, lineEnd)
	for scan < n {
		curEnd := findLineEnd(text, scan)
		p := scan
		sp := 0
		for p < curEnd && text[p] == ' ' && sp < 3 {
			p++
			sp++
		}
		cnt := 0
		for p < curEnd && text[p] == fenceChar {
			cnt++
			p++
		}
		if cnt >= fenceLen && strings.TrimSpace(text[p:curEnd]) == "" {
			return advancePastNewline(text, curEnd), true
		}
		scan = advancePastNewline(text, curEnd)
	}
	return n, true
}

// scanInlineBackticks skips an inline code span starting at pos.
func scanInlineBackticks(text string, pos int) int {
	n := len(text)
	tickLen := 0
	for pos+tickLen < n && text[pos+tickLen] == '`' {
		tickLen++
	}
	if tickLen == 0 {
		return pos + 1
	}

	i := pos + tickLen
	for i < n {
		// Inline code spans do not cross blank lines.
		if text[i] == '\n' {
			j := i + 1
			if j < n && text[j] == '\r' {
				j++
			}
			if j < n && text[j] == '\n' {
				return pos + tickLen
			}
		}
		if text[i] == '`' {
			closeLen := 0
			for i+closeLen < n && text[i+closeLen] == '`' {
				closeLen++
			}
			if closeLen == tickLen {
				return i + closeLen
			}
			i += closeLen
			continue
		}
		i++
	}
	return pos + tickLen
}

// matchXMLHarnessBlock checks whether text[pos:] opens a known harness XML block.
// Closed blocks match anywhere outside code; unclosed tags only match when anchored at line start.
func matchXMLHarnessBlock(text string, pos int, atLineStart bool) (int, bool) {
	if !atLineStart {
		// Also treat indented tags on their own line start as line-anchored.
		atLineStart = isOnlyHorizontalSpaceBeforeOnLine(text, pos)
	}
	rest := text[pos:]
	if len(rest) < 3 || rest[0] != '<' || rest[1] == '/' {
		return 0, false
	}

	for _, tag := range harnessXMLTags {
		if len(rest) < 1+len(tag)+1 {
			continue
		}
		if !strings.EqualFold(rest[1:1+len(tag)], tag) {
			continue
		}
		after := rest[1+len(tag)]
		if after != '>' && after != ' ' && after != '\t' && after != '\r' && after != '\n' {
			continue
		}

		// Locate end of opening tag '>'.
		openEndRel := strings.IndexByte(rest, '>')
		if openEndRel < 0 {
			return 0, false
		}
		// Reject suspiciously long or multi-line malformed opening tags without '>'.
		if strings.IndexByte(rest[:openEndRel], '\n') >= 0 && openEndRel > 256 {
			return 0, false
		}

		searchFrom := pos + openEndRel + 1
		if closeEnd, found := findClosingTag(text, searchFrom, tag); found {
			return closeEnd, true
		}
		if atLineStart {
			return len(text), true
		}
		return 0, false
	}
	return 0, false
}

func isOnlyHorizontalSpaceBeforeOnLine(text string, pos int) bool {
	for i := pos - 1; i >= 0; i-- {
		if text[i] == '\n' {
			return true
		}
		if text[i] != ' ' && text[i] != '\t' {
			return false
		}
	}
	return true
}

func findClosingTag(text string, from int, tag string) (int, bool) {
	lowerText := strings.ToLower(text[from:])
	needle := "</" + strings.ToLower(tag)
	offset := 0
	for {
		idx := strings.Index(lowerText[offset:], needle)
		if idx < 0 {
			return 0, false
		}
		absStart := from + offset + idx
		afterIdx := absStart + len(needle)
		p := afterIdx
		for p < len(text) && (text[p] == ' ' || text[p] == '\t' || text[p] == '\r' || text[p] == '\n') {
			p++
		}
		if p < len(text) && text[p] == '>' {
			return p + 1, true
		}
		offset += idx + len(needle)
	}
}

func findAgentsMDSectionEnd(text string, _ int, headingLineEnd int) int {
	scan := advancePastNewline(text, headingLineEnd)

	// Look for `<INSTRUCTIONS>` immediately following `# AGENTS.md instructions`.
	for scan < len(text) {
		lineEnd := findLineEnd(text, scan)
		trimmed := strings.TrimSpace(text[scan:lineEnd])
		if trimmed == "" {
			scan = advancePastNewline(text, lineEnd)
			continue
		}
		if strings.HasPrefix(strings.ToLower(trimmed), "<instructions") {
			if closeEnd, ok := findClosingTag(text, scan, "INSTRUCTIONS"); ok {
				return closeEnd
			}
		}
		break
	}
	return findNextTopLevelHarnessXML(text, headingLineEnd)
}

func findNextTopLevelHarnessXML(text string, from int) int {
	n := len(text)
	cursor := from
	for cursor < n {
		atLineStart := cursor == 0 || text[cursor-1] == '\n'
		if atLineStart {
			if endFence, ok := scanFencedCodeBlock(text, cursor); ok {
				cursor = endFence
				continue
			}
		}
		if text[cursor] == '`' {
			cursor = scanInlineBackticks(text, cursor)
			continue
		}
		if text[cursor] == '<' {
			if _, ok := matchXMLHarnessBlock(text, cursor, atLineStart); ok {
				return cursor
			}
		}
		cursor = nextInterestingPos(text, cursor)
	}
	return n
}

// isHarnessHeaderLine checks whether a single line (outside code fences) contains
// a billing-header or cc_* / cch= marker outside inline backticks.
func isHarnessHeaderLine(line string) bool {
	if line == "" {
		return false
	}
	locs := harnessHeaderLineRe.FindAllStringIndex(line, -1)
	if len(locs) == 0 {
		return false
	}

	// Collect inline backtick ranges on this line so `cc_version=1.0` inside prose isn't treated as a header line.
	type span struct{ s, e int }
	var codeSpans []span
	for i := 0; i < len(line); {
		if line[i] == '`' {
			end := scanInlineBackticks(line, i)
			if end > i+1 {
				codeSpans = append(codeSpans, span{i, end})
			}
			i = end
			continue
		}
		i++
	}

	for _, loc := range locs {
		inCode := false
		for _, cs := range codeSpans {
			if loc[0] >= cs.s && loc[0] < cs.e {
				inCode = true
				break
			}
		}
		if !inCode {
			return true
		}
	}
	return false
}
