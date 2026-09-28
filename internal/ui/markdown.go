// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"image/color"
	"strings"

	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

// Markdown renders the subset of Markdown that release notes actually use,
// so the Updates screen shows structure instead of raw source.
//
// Supported: ATX headings, bold, italic (read, but drawn plain — there is no
// oblique face), inline code, links (text shown, target dropped), bulleted
// and ordered lists, fenced code blocks, blockquotes (drawn plain),
// horizontal rules, tables (rows shown, separator skipped) and backslash
// escapes. Everything else — nested emphasis, reference links, raw HTML —
// degrades to visible literal text rather than vanishing.
//
// There is deliberately no bold or italic typeface involved: bold draws its
// run twice offset by one pixel (faux bold, face-agnostic), and links and
// code are set apart by colour. Unmatched markers render literally, so
// half-written formatting can never eat the sentence around it.
type MarkdownStyle struct {
	// Color is the body ink. Unset means the theme's muted text: notes are
	// secondary information.
	Color color.RGBA
	// Scale is the body glyph scale. Zero means the theme's body scale.
	// Headings render one scale up.
	Scale int
}

// mdKind is what one inline run is.
type mdKind int

const (
	mdNormal mdKind = iota
	mdBold
	mdCode
	mdLink
)

// mdSpan is one inline run.
type mdSpan struct {
	text string
	kind mdKind
}

// mdLine is one laid-out display line.
type mdLine struct {
	spans  []mdSpan
	scale  int
	indent int
	// rule draws a horizontal rule instead of text; blank advances a
	// half-line of air without drawing.
	rule  bool
	blank bool
}

// Markdown draws text in r, wrapping to its width, and never truncates: a
// clipped release note is worse than a long one. The height it needs is
// [MarkdownHeight], measured the same way, so a screen reserves exactly
// what this draws.
func Markdown(ctx *Context, r Rect, text string, style MarkdownStyle) {
	if r.W <= 0 || r.H <= 0 || text == "" {
		return
	}
	th := ctx.Theme
	scale := orInt(style.Scale, th.Body)
	base := or(style.Color, th.TextMuted)
	lines := layoutMarkdown(text, scale, th.Font, r.W)

	ctx.Canvas.PushClip(r)
	defer ctx.Canvas.PopClip()
	y := r.Y
	for _, ln := range lines {
		switch {
		case ln.blank:
			y += LineHeight(ln.scale, th.Font) / 2
		case ln.rule:
			mid := y + LineHeight(ln.scale, th.Font)/2
			ctx.Canvas.Fill(Rect{X: r.X, Y: mid, W: r.W, H: 1}, th.Border)
			y += LineHeight(ln.scale, th.Font)
		default:
			drawSpans(ctx, ln, r.X, y, base, th)
			y += LineHeight(ln.scale, th.Font)
		}
	}
}

// drawSpans draws one display line's runs left to right from x.
func drawSpans(ctx *Context, ln mdLine, x, y int, base color.RGBA, th *Theme) {
	x += ln.indent
	for _, s := range ln.spans {
		col := base
		switch s.kind {
		case mdCode:
			col = th.Text
		case mdLink:
			col = th.Accent
		case mdBold, mdNormal:
			col = base
		}
		if s.kind == mdBold {
			// Faux bold: the run twice, one pixel apart. The wrapper
			// reserves the extra pixel, so the overhang never clips.
			ctx.Canvas.Text(s.text, x, y, ln.scale, col)
			x += ctx.Canvas.Text(s.text, x+1, y, ln.scale, col)
			continue
		}
		x += ctx.Canvas.Text(s.text, x, y, ln.scale, col)
	}
}

// MarkdownHeight returns the pixel height Markdown needs for text at scale
// in f wrapped to width. Pass the same resolved scale both places: a zero
// style scale means the theme body, which this function cannot see.
func MarkdownHeight(text string, scale int, f viewer.Font, width int) int {
	if text == "" || width <= 0 || scale <= 0 {
		return 0
	}
	h := 0
	for _, ln := range layoutMarkdown(text, scale, f, width) {
		switch {
		case ln.blank:
			h += LineHeight(ln.scale, f) / 2
		default:
			h += LineHeight(ln.scale, f)
		}
	}
	return h
}

// layoutMarkdown parses text into display lines that fit width.
func layoutMarkdown(text string, scale int, f viewer.Font, width int) []mdLine {
	var out []mdLine
	inFence := false
	for _, raw := range strings.Split(text, "\n") {
		line := fold(raw, f)
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "```"):
			inFence = !inFence
		case inFence:
			out = append(out, wrapSpans([]mdSpan{{text: line, kind: mdCode}}, scale, f, width)...)
		case trimmed == "":
			out = append(out, mdLine{blank: true, scale: scale})
		case isRule(trimmed):
			out = append(out, mdLine{rule: true, scale: scale})
		case isTableSeparator(trimmed):
			// The |---|---| row carries no information; the data rows
			// around it render as plain lines.
		case isFootnoteDef(trimmed):
		case strings.HasPrefix(trimmed, "#"):
			if body, ok := cutHeading(trimmed); ok {
				out = append(out, wrapSpans(parseInline(body), scale+1, f, width)...)
			} else {
				out = append(out, wrapSpans(parseInline(line), scale, f, width)...)
			}
		case strings.HasPrefix(trimmed, ">"):
			out = append(out, wrapSpans(parseInline(strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))), scale, f, width)...)
		default:
			if prefix, rest, ok := cutListItem(trimmed); ok {
				pw := TextWidth(prefix, scale, f)
				prefixed := append([]mdSpan{{text: prefix, kind: mdNormal}}, parseInline(rest)...)
				out = append(out, wrapSpansIndented(prefixed, scale, f, width, pw)...)
			} else {
				out = append(out, wrapSpans(parseInline(line), scale, f, width)...)
			}
		}
	}
	return out
}

// cutHeading strips ATX markers ("## Hi ##" → "Hi"), or false when the line
// is not a heading (just hashes, or a hash with no following space).
func cutHeading(s string) (string, bool) {
	i := 0
	for i < len(s) && s[i] == '#' {
		i++
	}
	if i == 0 || i > 6 || (i < len(s) && s[i] != ' ' && s[i] != '\t') {
		return "", false
	}
	body := strings.TrimSpace(s[i:])
	body = strings.TrimSpace(strings.TrimRight(body, "#"))
	if body == "" {
		return "", false
	}
	return body, true
}

// isRule reports a horizontal rule: three or more of -, _ or * and nothing
// else (spaces allowed).
func isRule(s string) bool {
	stripped := strings.ReplaceAll(s, " ", "")
	if len(stripped) < 3 {
		return false
	}
	for i := 0; i < len(stripped); i++ {
		if stripped[i] != stripped[0] || (stripped[i] != '-' && stripped[i] != '_' && stripped[i] != '*') {
			return false
		}
	}
	return true
}

// isTableSeparator reports a Markdown table's |---|---| row.
func isTableSeparator(s string) bool {
	if !strings.Contains(s, "-") || !strings.Contains(s, "|") {
		return false
	}
	for _, r := range s {
		if r != ' ' && r != '|' && r != '-' && r != ':' {
			return false
		}
	}
	return true
}

// isFootnoteDef reports a reference definition ("[id]: https://…"), whose
// URL has nowhere to go in a static rendering.
func isFootnoteDef(s string) bool {
	if !strings.HasPrefix(s, "[") {
		return false
	}
	end := strings.Index(s, "]:")
	return end > 1 && strings.TrimSpace(s[end+2:]) != ""
}

// cutListItem splits a list marker ("- ", "1. ", "2) ") from its text,
// returning the display prefix. Checkbox markers pass through literally:
// "[ ]" and "[x]" are ASCII and survive every face.
func cutListItem(s string) (prefix, rest string, ok bool) {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	start := i
	if i < len(s) && (s[i] == '-' || s[i] == '*' || s[i] == '+') {
		i++
	} else {
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == start || i >= len(s) || (s[i] != '.' && s[i] != ')') {
			return "", "", false
		}
		i++
	}
	if i >= len(s) || (s[i] != ' ' && s[i] != '\t') {
		return "", "", false
	}
	marker := s[start:i]
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return marker + " ", s[i:], true
}

// parseInline splits s into styled runs. It is the only place emphasis is
// understood, so links inside bold text stay literal — one level is all
// release notes need, and the alternative is a state machine nobody can
// follow.
func parseInline(s string) []mdSpan {
	var out []mdSpan
	emit := func(text string, kind mdKind) {
		if text == "" {
			return
		}
		if n := len(out); n > 0 && out[n-1].kind == kind {
			out[n-1].text += text
			return
		}
		out = append(out, mdSpan{text: text, kind: kind})
	}
	runes := []rune(s)
	isAlnum := func(r rune) bool {
		return r == '_' || 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9'
	}
	hasPrefixAt := func(i int, p string) bool {
		pr := []rune(p)
		if i+len(pr) > len(runes) {
			return false
		}
		for k, r := range pr {
			if runes[i+k] != r {
				return false
			}
		}
		return true
	}
	findCloser := func(from int, marker string) int {
		for j := from; j+len([]rune(marker)) <= len(runes); j++ {
			if hasPrefixAt(j, marker) {
				return j
			}
		}
		return -1
	}
	var plain strings.Builder
	flush := func() {
		if plain.Len() > 0 {
			out = append(out, splitURLs(plain.String())...)
			plain.Reset()
		}
	}
	i := 0
	for i < len(runes) {
		r := runes[i]
		// Backslash escape: the next character is literal.
		if r == '\\' && i+1 < len(runes) {
			plain.WriteRune(runes[i+1])
			i += 2
			continue
		}
		// Inline code spans pair with themselves; without a closer the
		// backtick is literal text.
		if r == '`' {
			if j := findCloser(i+1, "`"); j >= 0 {
				flush()
				emit(string(runes[i+1:j]), mdCode)
				i = j + 1
				continue
			}
			plain.WriteRune(r)
			i++
			continue
		}
		// Bold pairs with itself; unmatched markers are literal.
		if hasPrefixAt(i, "**") || hasPrefixAt(i, "__") {
			marker := string(runes[i : i+2])
			if j := findCloser(i+2, marker); j >= 0 {
				flush()
				emit(string(runes[i+2:j]), mdBold)
				i = j + 2
				continue
			}
			plain.WriteString(marker)
			i += 2
			continue
		}
		// Single-character emphasis renders plain (there is no oblique
		// face), but the markers are still consumed. Inside a word
		// (foo_bar) they are literal, matching commonmark's rule.
		if r == '*' || r == '_' || r == '~' {
			marker := string(r)
			doubled := string(rune(r)) + string(rune(r))
			prevOK := i == 0 || !isAlnum(runes[i-1])
			if prevOK {
				if j := findCloser(i+1, marker); j >= 0 && (j+1 >= len(runes) || !isAlnum(runes[j+1]) || hasPrefixAt(j, doubled)) {
					flush()
					emit(string(runes[i+1:j]), mdNormal)
					i = j + 1
					continue
				}
			}
			plain.WriteRune(r)
			i++
			continue
		}
		// Links: [text](target). The target is dropped — a static label
		// cannot follow one — and the text keeps any emphasis it had by
		// parsing one level down. Anything malformed stays literal.
		if r == '[' {
			if text, target, next, ok := cutLink(runes, i); ok {
				_ = target
				flush()
				for _, inner := range parseInline(text) {
					if inner.kind == mdCode {
						emit(inner.text, mdCode)
					} else {
						emit(inner.text, mdLink)
					}
				}
				i = next
				continue
			}
			plain.WriteRune(r)
			i++
			continue
		}
		// Autolinks <https://…>.
		if r == '<' {
			if j := findCloser(i+1, ">"); j >= 0 {
				inner := string(runes[i+1 : j])
				if strings.HasPrefix(inner, "http://") || strings.HasPrefix(inner, "https://") {
					flush()
					emit(inner, mdLink)
					i = j + 1
					continue
				}
			}
			plain.WriteRune(r)
			i++
			continue
		}
		plain.WriteRune(r)
		i++
	}
	flush()
	if len(out) == 0 {
		return []mdSpan{{text: s}}
	}
	return out
}

// cutLink parses "[text](target)" at runes[i] (which is '['), returning the
// text, the target and the index past the closing paren.
func cutLink(runes []rune, i int) (text, target string, next int, ok bool) {
	depth := 0
	j := i
	for ; j < len(runes); j++ {
		switch runes[j] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				goto textDone
			}
		}
	}
	return "", "", 0, false
textDone:
	text = string(runes[i+1 : j])
	j++
	if j >= len(runes) || runes[j] != '(' {
		return "", "", 0, false
	}
	k := j + 1
	for ; k < len(runes) && runes[k] != ')'; k++ {
	}
	if k >= len(runes) {
		return "", "", 0, false
	}
	return text, strings.TrimSpace(string(runes[j+1 : k])), k + 1, true
}

// splitURLs cuts bare https://… tokens out of a plain segment as link runs.
// It runs on plain segments only, so URLs inside code spans and link targets
// are untouched. Trailing punctuation belongs to the sentence, not the URL.
func splitURLs(s string) []mdSpan {
	var out []mdSpan
	for {
		idx := -1
		for _, p := range []string{"https://", "http://"} {
			if j := strings.Index(s, p); j >= 0 && (idx < 0 || j < idx) {
				idx = j
			}
		}
		if idx < 0 {
			if s != "" {
				out = append(out, mdSpan{text: s, kind: mdNormal})
			}
			return out
		}
		if idx > 0 {
			out = append(out, mdSpan{text: s[:idx], kind: mdNormal})
			s = s[idx:]
		}
		end := len(s)
		for k, r := range s {
			if r == ' ' || r == '\t' {
				end = k
				break
			}
		}
		tok := s[:end]
		url := strings.TrimRight(tok, ".,;:!?)]>")
		out = append(out, mdSpan{text: url, kind: mdLink})
		s = tok[len(url):] + s[end:]
	}
}

// mdAtom is one rune with its style, so a long run can break mid-word
// exactly like [splitWord] does.
type mdAtom struct {
	r    rune
	kind mdKind
}

// wrapSpans packs runs into display lines that fit width.
func wrapSpans(spans []mdSpan, scale int, f viewer.Font, width int) []mdLine {
	return wrapSpansIndented(spans, scale, f, width, 0)
}

// wrapSpansIndented is [wrapSpans] with a hanging indent for wrapped list
// lines: the first line starts at the margin (carrying its bullet), every
// continuation starts under the text.
func wrapSpansIndented(spans []mdSpan, scale int, f viewer.Font, width, indent int) []mdLine {
	var atoms []mdAtom
	for _, s := range spans {
		for _, r := range s.text {
			atoms = append(atoms, mdAtom{r: r, kind: s.kind})
		}
	}
	if len(atoms) == 0 {
		return []mdLine{{scale: scale}}
	}
	spanW := func(a mdAtom) int {
		w := advanceAt(f, scale, a.r)
		if a.kind == mdBold {
			w++ // the faux-bold overhang
		}
		return w
	}
	var lines []mdLine
	var cur []mdAtom
	curW := 0
	first := true
	avail := func() int {
		if first {
			return width
		}
		return width - indent
	}
	flush := func() {
		lines = append(lines, packLine(cur, scale, first, indent))
		cur, curW, first = nil, 0, false
	}
	for _, a := range atoms {
		if a.r == ' ' {
			// Spaces ride at the end of a line but never start one: a
			// wrapped line that began with the gap looks indented for no
			// reason, and the width it would reserve is wrong anyway.
			if len(cur) == 0 {
				continue
			}
			if curW+spanW(a) > avail() {
				flush()
				continue
			}
			cur = append(cur, a)
			curW += spanW(a)
			continue
		}
		if len(cur) > 0 && curW+spanW(a) > avail() {
			// Break before the atom, dropping a trailing space: it was a
			// separator, not content.
			for len(cur) > 0 && cur[len(cur)-1].r == ' ' {
				curW -= spanW(cur[len(cur)-1])
				cur = cur[:len(cur)-1]
			}
			flush()
		}
		cur = append(cur, a)
		curW += spanW(a)
	}
	// Trailing spaces are separators, not content.
	for len(cur) > 0 && cur[len(cur)-1].r == ' ' {
		cur = cur[:len(cur)-1]
	}
	if len(cur) > 0 || first {
		flush()
	}
	return lines
}

// packLine rebuilds styled runs from atoms, merging neighbours of a kind.
func packLine(atoms []mdAtom, scale int, first bool, indent int) mdLine {
	ln := mdLine{scale: scale}
	if !first {
		ln.indent = indent
	}
	var b strings.Builder
	kind := mdNormal
	started := false
	flushRun := func() {
		if b.Len() > 0 {
			ln.spans = append(ln.spans, mdSpan{text: b.String(), kind: kind})
			b.Reset()
		}
	}
	for _, a := range atoms {
		if !started || a.kind != kind {
			flushRun()
			kind, started = a.kind, true
		}
		b.WriteRune(a.r)
	}
	flushRun()
	return ln
}
