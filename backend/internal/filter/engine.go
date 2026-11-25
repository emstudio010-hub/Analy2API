package filter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Rule defines a single filter/rewrite/replace rule in the sanitization engine.
type Rule struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Pattern       string `json:"pattern"`
	Replacement   string `json:"replacement"`
	Scope         string `json:"scope"`
	Description   string `json:"description"`
	IsRegex       bool   `json:"is_regex"`
	CaseSensitive bool   `json:"case_sensitive"`
	WordBoundary  bool   `json:"word_boundary"`
	Enabled       bool   `json:"enabled"`
	IsBuiltin     bool   `json:"is_builtin"`
	Priority      int    `json:"priority"`
}

// RuleHit aggregates how many times a specific rule matched during sanitization.
type RuleHit struct {
	RuleID      int64  `json:"rule_id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Pattern     string `json:"pattern"`
	Replacement string `json:"replacement"`
	Count       int    `json:"count"`
}

// Stats captures execution telemetry for a sanitization or preview run.
type Stats struct {
	PruneHits     int       `json:"prune_hits"`
	RewriteHits   int       `json:"rewrite_hits"`
	ReplaceHits   int       `json:"replace_hits"`
	FieldsTouched int       `json:"fields_touched"`
	Hits          []RuleHit `json:"hits"`
}

// PreviewResult describes the before/after output and hit metrics for a preview test.
type PreviewResult struct {
	Input   string `json:"input"`
	Output  string `json:"output"`
	Mode    string `json:"mode"`
	Changed bool   `json:"changed"`
	Stats   Stats  `json:"stats"`
}

type compiledRule struct {
	rule       Rule
	kind       string
	scopes     map[string]struct{}
	allScopes  bool
	isLiteral  bool
	re         *regexp.Regexp
	orderIndex int
}

// Engine executes the three-tier sanitization pipeline safely across concurrent requests.
type Engine struct {
	mu       sync.RWMutex
	rawRules []Rule
	compiled []compiledRule
}

// NewEngine constructs an Engine initialized with the supplied rules.
func NewEngine(rules []Rule) *Engine {
	e := &Engine{}
	e.Reload(rules)
	return e
}

// NewDefaultEngine constructs an Engine loaded with DefaultRules().
func NewDefaultEngine() *Engine {
	return NewEngine(DefaultRules())
}

// Rules returns a defensive copy of the currently loaded rules.
func (e *Engine) Rules() []Rule {
	if e == nil {
		return nil
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return CloneRules(e.rawRules)
}

// Reload hot-swaps the active rule set in a thread-safe manner.
func (e *Engine) Reload(rules []Rule) {
	if e == nil {
		return
	}
	rawCopy := CloneRules(rules)
	compiled := compileRules(rawCopy)

	e.mu.Lock()
	e.rawRules = rawCopy
	e.compiled = compiled
	e.mu.Unlock()
}

func (e *Engine) snapshotCompiled() []compiledRule {
	if e == nil {
		return nil
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.compiled
}

// SanitizeText applies scope-appropriate sanitization rules to a single text string.
// For scopeTarget == "user", only detected harness-injected fragments are modified;
// natural user questions and pasted source code remain completely unchanged.
func (e *Engine) SanitizeText(scopeTarget string, text string) (string, []RuleHit) {
	if e == nil || text == "" {
		return text, nil
	}
	target := normalizeScopeTarget(scopeTarget)
	if target == "" {
		return text, nil
	}

	rules := e.snapshotCompiled()
	if len(rules) == 0 {
		return text, nil
	}

	if target == ScopeUser {
		if !HasHarnessInjection(text) {
			return text, nil
		}
		acc := newHitAccumulator(rules)
		out := applyToHarnessSpans(text, func(fragment string) string {
			sanitized, hits := applyCompiledToText(rules, ScopeUser, fragment)
			acc.addMap(hits)
			return sanitized
		})
		return out, acc.toHits()
	}

	out, counts := applyCompiledToText(rules, target, text)
	acc := newHitAccumulator(rules)
	acc.addMap(counts)
	return out, acc.toHits()
}

// SanitizePayload sanitizes a JSON request payload for "openai", "anthropic", or "responses".
func (e *Engine) SanitizePayload(protocol string, raw []byte) ([]byte, *Stats, error) {
	if e == nil {
		return append([]byte(nil), raw...), &Stats{}, nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil, errors.New("empty request payload")
	}

	doc, err := decodeJSONObject(trimmed)
	if err != nil {
		return nil, nil, err
	}

	proto, err := resolveProtocol(protocol, doc)
	if err != nil {
		return nil, nil, err
	}

	rules := e.snapshotCompiled()
	acc := newHitAccumulator(rules)
	fieldsTouched := 0

	sanitizeStr := func(target string, s string) (string, bool) {
		if s == "" || len(rules) == 0 {
			return s, false
		}
		var out string
		var counts map[int]int
		if target == ScopeUser {
			if !HasHarnessInjection(s) {
				return s, false
			}
			counts = make(map[int]int)
			out = applyToHarnessSpans(s, func(fragment string) string {
				res, h := applyCompiledToText(rules, ScopeUser, fragment)
				for idx, c := range h {
					counts[idx] += c
				}
				return res
			})
		} else {
			out, counts = applyCompiledToText(rules, target, s)
		}
		acc.addMap(counts)
		if out != s {
			fieldsTouched++
			return out, true
		}
		return s, false
	}

	var sanitizeContent func(target string, val any) (any, bool)
	sanitizeContent = func(target string, val any) (any, bool) {
		switch v := val.(type) {
		case string:
			return sanitizeStr(target, v)
		case []any:
			anyChanged := false
			for _, item := range v {
				blk, ok := item.(map[string]any)
				if !ok {
					continue
				}
				typ := strings.ToLower(strings.TrimSpace(stringField(blk, "type")))
				if !shouldSanitizeContentBlock(target, typ) {
					continue
				}
				if txt, ok := blk["text"].(string); ok {
					if out, changed := sanitizeStr(target, txt); changed {
						blk["text"] = out
						anyChanged = true
					}
				}
			}
			return v, anyChanged
		default:
			return val, false
		}
	}

	var sanitizeToolValue func(val any)
	sanitizeToolValue = func(val any) {
		switch v := val.(type) {
		case map[string]any:
			for k, item := range v {
				if k == "description" || k == "title" {
					if s, ok := item.(string); ok {
						if out, changed := sanitizeStr(ScopeTool, s); changed {
							v[k] = out
						}
						continue
					}
				}
				sanitizeToolValue(item)
			}
		case []any:
			for _, item := range v {
				sanitizeToolValue(item)
			}
		}
	}

	switch proto {
	case "openai":
		sanitizeMessagesArray(doc["messages"], sanitizeContent)
		sanitizeToolValue(doc["tools"])
		sanitizeToolValue(doc["functions"])
	case "anthropic":
		if sysVal, ok := doc["system"]; ok && sysVal != nil {
			if out, changed := sanitizeContent(ScopeSystem, sysVal); changed {
				doc["system"] = out
			}
		}
		sanitizeMessagesArray(doc["messages"], sanitizeContent)
		sanitizeToolValue(doc["tools"])
	case "responses":
		sanitizeResponsesTopLevel(doc, sanitizeStr, sanitizeContent)
		sanitizeToolValue(doc["tools"])
	}

	stats := acc.toStats(fieldsTouched)
	if fieldsTouched == 0 {
		return append([]byte(nil), raw...), &stats, nil
	}

	encoded, err := encodeJSONCompact(doc)
	if err != nil {
		return nil, nil, err
	}
	return encoded, &stats, nil
}

// Preview runs the engine against an input string in the specified mode
// ("auto", "json", "system", "tool", "user_harness") for interactive testing.
func (e *Engine) Preview(input string, mode string, protocol string) (*PreviewResult, error) {
	resolvedMode, err := resolvePreviewMode(input, mode)
	if err != nil {
		return nil, err
	}

	result := &PreviewResult{
		Input: input,
		Mode:  resolvedMode,
	}

	if resolvedMode == "json" {
		outBytes, stats, err := e.SanitizePayload(protocol, []byte(input))
		if err != nil {
			return nil, err
		}
		result.Stats = *stats
		if stats.FieldsTouched == 0 {
			result.Output = input
			result.Changed = false
			return result, nil
		}
		if strings.Contains(input, "\n") {
			var pretty bytes.Buffer
			if err := json.Indent(&pretty, outBytes, "", "  "); err == nil {
				result.Output = pretty.String()
			} else {
				result.Output = string(outBytes)
			}
		} else {
			result.Output = string(outBytes)
		}
		result.Changed = true
		return result, nil
	}

	var scopeTarget string
	switch resolvedMode {
	case "system":
		scopeTarget = ScopeSystem
	case "tool":
		scopeTarget = ScopeTool
	case "user_harness":
		scopeTarget = ScopeUser
	default:
		return nil, fmt.Errorf("unsupported preview mode %q", mode)
	}

	out, hits := e.SanitizeText(scopeTarget, input)
	stats := buildStatsFromHits(hits, out != input)
	result.Output = out
	result.Changed = out != input
	result.Stats = stats
	return result, nil
}

func resolvePreviewMode(input string, mode string) (string, error) {
	m := strings.ToLower(strings.TrimSpace(mode))
	switch m {
	case "", "auto":
		trimmed := strings.TrimSpace(input)
		if strings.HasPrefix(trimmed, "{") {
			if _, err := decodeJSONObject([]byte(trimmed)); err == nil {
				return "json", nil
			}
		}
		if looksLikeMixedUserHarness(input) {
			return "user_harness", nil
		}
		return "system", nil
	case "json", "system", "tool":
		return m, nil
	case "user_harness", "user":
		return "user_harness", nil
	default:
		return "", fmt.Errorf("unsupported preview mode %q", mode)
	}
}

func looksLikeMixedUserHarness(input string) bool {
	if !HasHarnessInjection(input) {
		return false
	}
	lower := strings.ToLower(input)
	systemSignals := []string{
		"you are claude code",
		"you are a coding agent",
		"you are an ai agent",
		"you are a claude agent",
		"within this context, codex refers to",
		"# doing tasks",
		"# safety",
	}
	for _, sig := range systemSignals {
		if strings.Contains(lower, sig) {
			return false
		}
	}
	blockSignals := []string{
		"<system-reminder",
		"<permissions instructions",
		"<environment_context",
		"<collaboration_mode",
		"<skills_instructions",
		"<plugins_instructions",
		"# agents.md instructions",
		"# claudemd",
	}
	for _, sig := range blockSignals {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

func sanitizeMessagesArray(msgsVal any, sanitizeContent func(string, any) (any, bool)) {
	msgs, ok := msgsVal.([]any)
	if !ok {
		return
	}
	for _, item := range msgs {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(stringField(msg, "role")))
		switch role {
		case "system", "developer":
			if out, changed := sanitizeContent(ScopeSystem, msg["content"]); changed {
				msg["content"] = out
			}
		case "user":
			if out, changed := sanitizeContent(ScopeUser, msg["content"]); changed {
				msg["content"] = out
			}
		}
	}
}

func sanitizeResponsesInput(
	inputVal any,
	sanitizeStr func(string, string) (string, bool),
	sanitizeContent func(string, any) (any, bool),
) {
	items, ok := inputVal.([]any)
	if !ok {
		return
	}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(stringField(m, "role")))
		typ := strings.ToLower(strings.TrimSpace(stringField(m, "type")))

		switch {
		case role == "assistant" || role == "tool" ||
			typ == "function_call" || typ == "function_call_output" ||
			typ == "reasoning" || typ == "item_reference" || typ == "compaction":
			continue
		case role == "system" || role == "developer":
			if content, exists := m["content"]; exists {
				if out, changed := sanitizeContent(ScopeSystem, content); changed {
					m["content"] = out
				}
			} else if txt, ok := m["text"].(string); ok {
				if out, changed := sanitizeStr(ScopeSystem, txt); changed {
					m["text"] = out
				}
			}
		case role == "user" || role == "":
			if content, exists := m["content"]; exists {
				if out, changed := sanitizeContent(ScopeUser, content); changed {
					m["content"] = out
				}
			} else if txt, ok := m["text"].(string); ok && (typ == "" || typ == "input_text" || typ == "text" || typ == "message") {
				if out, changed := sanitizeStr(ScopeUser, txt); changed {
					m["text"] = out
				}
			}
		}
	}
}

func sanitizeResponsesTopLevel(
	doc map[string]any,
	sanitizeStr func(string, string) (string, bool),
	sanitizeContent func(string, any) (any, bool),
) {
	if instr, ok := doc["instructions"]; ok && instr != nil {
		if out, changed := sanitizeContent(ScopeSystem, instr); changed {
			doc["instructions"] = out
		}
	}
	if s, ok := doc["input"].(string); ok {
		if out, changed := sanitizeStr(ScopeUser, s); changed {
			doc["input"] = out
		}
		return
	}
	sanitizeResponsesInput(doc["input"], sanitizeStr, sanitizeContent)
}

func shouldSanitizeContentBlock(target string, blockType string) bool {
	if blockType == "" || blockType == "text" || blockType == "input_text" {
		return true
	}
	if target == ScopeSystem && blockType == "output_text" {
		return true
	}
	return false
}

func resolveProtocol(protocol string, doc map[string]any) (string, error) {
	p := strings.ToLower(strings.TrimSpace(protocol))
	switch p {
	case "openai", "chat", "chat/completions", "openai_chat":
		return "openai", nil
	case "anthropic", "messages":
		return "anthropic", nil
	case "responses", "openai_responses":
		return "responses", nil
	case "", "auto":
		if _, hasInstr := doc["instructions"]; hasInstr {
			return "responses", nil
		}
		if _, hasInput := doc["input"]; hasInput {
			return "responses", nil
		}
		if _, hasSys := doc["system"]; hasSys {
			return "anthropic", nil
		}
		if tools, ok := doc["tools"].([]any); ok {
			for _, t := range tools {
				if tm, ok := t.(map[string]any); ok {
					if _, hasSchema := tm["input_schema"]; hasSchema {
						return "anthropic", nil
					}
				}
			}
		}
		return "openai", nil
	default:
		return "", fmt.Errorf("unsupported protocol %q", protocol)
	}
}

func decodeJSONObject(raw []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode json payload: %w", err)
	}
	if doc == nil {
		return nil, errors.New("json payload must be an object")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("trailing data after json payload")
	}
	return doc, nil
}

func encodeJSONCompact(doc map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("encode json payload: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func stringField(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func compileRules(rules []Rule) []compiledRule {
	var compiled []compiledRule
	for _, r := range rules {
		if !r.Enabled || r.Pattern == "" {
			continue
		}
		cr, ok := compileSingleRule(r)
		if !ok {
			continue
		}
		compiled = append(compiled, cr)
	}

	sort.SliceStable(compiled, func(i, j int) bool {
		ki, kj := kindRank(compiled[i].kind), kindRank(compiled[j].kind)
		if ki != kj {
			return ki < kj
		}
		if compiled[i].rule.Priority != compiled[j].rule.Priority {
			return compiled[i].rule.Priority < compiled[j].rule.Priority
		}
		li, lj := len(compiled[i].rule.Pattern), len(compiled[j].rule.Pattern)
		if li != lj {
			return li > lj
		}
		return compiled[i].rule.ID < compiled[j].rule.ID
	})

	for i := range compiled {
		compiled[i].orderIndex = i
	}
	return compiled
}

func compileSingleRule(r Rule) (compiledRule, bool) {
	kind := strings.ToLower(strings.TrimSpace(r.Kind))
	if kind == "" {
		kind = KindReplace
	}
	allScopes, scopes := parseRuleScopes(r.Scope)
	cr := compiledRule{
		rule:      r,
		kind:      kind,
		scopes:    scopes,
		allScopes: allScopes,
	}

	if !r.IsRegex && r.CaseSensitive && !r.WordBoundary {
		cr.isLiteral = true
		return cr, true
	}

	var expr string
	if r.IsRegex {
		expr = r.Pattern
		if !r.CaseSensitive && !hasCaseFlag(expr) {
			expr = "(?i)" + expr
		}
	} else {
		expr = regexp.QuoteMeta(r.Pattern)
		if r.WordBoundary {
			first, _ := utf8.DecodeRuneInString(r.Pattern)
			last, _ := utf8.DecodeLastRuneInString(r.Pattern)
			if isWordRune(first) {
				expr = `\b` + expr
			}
			if isWordRune(last) {
				expr = expr + `\b`
			}
		}
		if !r.CaseSensitive {
			expr = "(?i)" + expr
		}
	}

	re, err := regexp.Compile(expr)
	if err != nil {
		return compiledRule{}, false
	}
	cr.re = re
	return cr, true
}

func kindRank(kind string) int {
	switch kind {
	case KindPrune:
		return 0
	case KindRewrite:
		return 1
	case KindReplace:
		return 2
	default:
		return 3
	}
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func hasCaseFlag(expr string) bool {
	return strings.HasPrefix(expr, "(?i)") || strings.HasPrefix(expr, "(?i:") || strings.HasPrefix(expr, "(?im") || strings.HasPrefix(expr, "(?is")
}

func parseRuleScopes(scope string) (bool, map[string]struct{}) {
	s := strings.ToLower(strings.TrimSpace(scope))
	if s == "" || s == "all" || s == ScopeAllInjected {
		return true, nil
	}
	set := make(map[string]struct{})
	for _, part := range strings.Split(s, ",") {
		norm := normalizeScopeTarget(part)
		if norm == "all" || norm == ScopeAllInjected {
			return true, nil
		}
		if norm != "" {
			set[norm] = struct{}{}
		}
	}
	if len(set) == 0 {
		return true, nil
	}
	return false, set
}

func normalizeScopeTarget(target string) string {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "system", "developer", "instructions":
		return ScopeSystem
	case "tool", "tools":
		return ScopeTool
	case "user", "user_harness":
		return ScopeUser
	case "all", "all_injected":
		return ScopeAllInjected
	default:
		return ""
	}
}

func (cr *compiledRule) matchesScope(target string) bool {
	if cr.allScopes {
		return true
	}
	_, ok := cr.scopes[target]
	return ok
}

var collapseExtraNewlinesRe = regexp.MustCompile(`\n{3,}`)

func applyCompiledToText(rules []compiledRule, target string, text string) (string, map[int]int) {
	if text == "" || len(rules) == 0 {
		return text, nil
	}
	orig := text
	current := text
	pruneMatched := false
	var counts map[int]int

	for i := range rules {
		cr := &rules[i]
		if !cr.matchesScope(target) {
			continue
		}
		var n int
		current, n = cr.apply(current)
		if n > 0 {
			if counts == nil {
				counts = make(map[int]int)
			}
			counts[i] += n
			if cr.kind == KindPrune {
				pruneMatched = true
			}
		}
	}

	if pruneMatched && current != orig {
		if !strings.HasPrefix(orig, "\r") && !strings.HasPrefix(orig, "\n") {
			current = strings.TrimLeft(current, "\r\n")
		}
		if strings.TrimSpace(current) == "" {
			current = ""
		} else if strings.Contains(current, "\n\n\n") {
			current = collapseExtraNewlinesRe.ReplaceAllString(current, "\n\n")
		}
	}

	return current, counts
}

func (cr *compiledRule) apply(s string) (string, int) {
	if cr.isLiteral {
		count := strings.Count(s, cr.rule.Pattern)
		if count == 0 {
			return s, 0
		}
		return strings.ReplaceAll(s, cr.rule.Pattern, cr.rule.Replacement), count
	}

	if cr.re == nil {
		return s, 0
	}

	if !cr.rule.IsRegex || !strings.Contains(cr.rule.Replacement, "$") {
		count := 0
		out := cr.re.ReplaceAllStringFunc(s, func(_ string) string {
			count++
			return cr.rule.Replacement
		})
		if count == 0 {
			return s, 0
		}
		return out, count
	}

	matches := cr.re.FindAllStringIndex(s, -1)
	if len(matches) == 0 {
		return s, 0
	}
	return cr.re.ReplaceAllString(s, cr.rule.Replacement), len(matches)
}

type hitAccumulator struct {
	rules  []compiledRule
	counts []int
}

func newHitAccumulator(rules []compiledRule) *hitAccumulator {
	return &hitAccumulator{
		rules:  rules,
		counts: make([]int, len(rules)),
	}
}

func (a *hitAccumulator) addMap(m map[int]int) {
	for idx, c := range m {
		if idx >= 0 && idx < len(a.counts) {
			a.counts[idx] += c
		}
	}
}

func (a *hitAccumulator) toHits() []RuleHit {
	var hits []RuleHit
	for i, c := range a.counts {
		if c <= 0 {
			continue
		}
		r := a.rules[i].rule
		hits = append(hits, RuleHit{
			RuleID:      r.ID,
			Name:        r.Name,
			Kind:        a.rules[i].kind,
			Pattern:     r.Pattern,
			Replacement: r.Replacement,
			Count:       c,
		})
	}
	return hits
}

func (a *hitAccumulator) toStats(fieldsTouched int) Stats {
	hits := a.toHits()
	stats := Stats{
		FieldsTouched: fieldsTouched,
		Hits:          hits,
	}
	for _, h := range hits {
		switch h.Kind {
		case KindPrune:
			stats.PruneHits += h.Count
		case KindRewrite:
			stats.RewriteHits += h.Count
		case KindReplace:
			stats.ReplaceHits += h.Count
		}
	}
	return stats
}

func buildStatsFromHits(hits []RuleHit, changed bool) Stats {
	stats := Stats{
		Hits: hits,
	}
	if changed {
		stats.FieldsTouched = 1
	}
	for _, h := range hits {
		switch h.Kind {
		case KindPrune:
			stats.PruneHits += h.Count
		case KindRewrite:
			stats.RewriteHits += h.Count
		case KindReplace:
			stats.ReplaceHits += h.Count
		}
	}
	return stats
}
