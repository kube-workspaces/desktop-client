// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"strings"
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

func mdText(spans []mdSpan) string {
	var b strings.Builder
	for _, s := range spans {
		b.WriteString(s.text)
	}
	return b.String()
}

func TestParseInlineStyles(t *testing.T) {
	spans := parseInline("fix **bold** and `code` and [link](https://x.example) end")
	var kinds []mdKind
	for _, s := range spans {
		kinds = append(kinds, s.kind)
	}
	wantKinds := []mdKind{mdNormal, mdBold, mdNormal, mdCode, mdNormal, mdLink, mdNormal}
	if len(kinds) != len(wantKinds) {
		t.Fatalf("kinds = %v, want %v (text %q)", kinds, wantKinds, mdText(spans))
	}
	for i := range kinds {
		if kinds[i] != wantKinds[i] {
			t.Fatalf("kinds = %v, want %v (text %q)", kinds, wantKinds, mdText(spans))
		}
	}
	if got := mdText(spans); got != "fix bold and code and link end" {
		t.Fatalf("markers leaked into %q", got)
	}
}

func TestParseInlineUnmatchedMarkersStayLiteral(t *testing.T) {
	for _, s := range []string{"a **oops", "a `oops", "a [oops", "a [oops](nope", "2 * 3 = 6"} {
		if got := mdText(parseInline(s)); got != s {
			t.Fatalf("parseInline(%q) = %q, want it untouched", s, got)
		}
	}
}

func TestParseInlineGuards(t *testing.T) {
	// snake_case keeps its underscore; escaped markers stay literal.
	if got := mdText(parseInline("foo_bar")); got != "foo_bar" {
		t.Fatalf("intraword underscore parsed: %q", got)
	}
	if got := mdText(parseInline(`\*\*hi\*\*`)); got != "**hi**" {
		t.Fatalf("escape failed: %q", got)
	}
	// *italic* markers are consumed even though there is no oblique face.
	if got := mdText(parseInline("*hi*")); got != "hi" {
		t.Fatalf("italic markers leaked: %q", got)
	}
}

func TestParseInlineBareURL(t *testing.T) {
	spans := parseInline("see https://example.com/x, ok")
	if len(spans) != 3 || spans[1].kind != mdLink || spans[1].text != "https://example.com/x" {
		t.Fatalf("bare URL not linked: %+v", spans)
	}
	if spans[2].text != ", ok" {
		t.Fatalf("trailing punctuation joined the URL: %+v", spans)
	}
}

func TestMarkdownBlocks(t *testing.T) {
	f := viewer.RetroFont
	lines := layoutMarkdown("## Hi\n\n- a\n- b\n\n1. one\n\n> quoted\n\n---\n\n```\ncode()\n```\n\n| a | b |\n|---|---|\n\n[^1]: https://x", 1, f, 1000)
	var texts []string
	for _, l := range lines {
		if l.blank || l.rule {
			texts = append(texts, "<gap>")
			continue
		}
		texts = append(texts, mdText(l.spans))
	}
	joined := strings.Join(texts, "\n")
	for _, want := range []string{"Hi", "- a", "- b", "1. one", "quoted", "code()", "| a | b |"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("block %q missing from:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "---") || strings.Contains(joined, "[^1]") {
		t.Fatalf("separator or footnote leaked into:\n%s", joined)
	}
	// The heading renders one scale up.
	for _, l := range lines {
		if !l.blank && !l.rule && mdText(l.spans) == "Hi" && l.scale != 2 {
			t.Fatalf("heading scale = %d, want 2", l.scale)
		}
	}
}

func TestMarkdownListHangingIndent(t *testing.T) {
	f := viewer.RetroFont
	// Narrow enough to force a wrap inside the item.
	lines := layoutMarkdown("- alpha beta gamma delta", 1, f, 12*viewer.GlyphAdvance)
	if len(lines) < 2 {
		t.Fatalf("expected a wrapped list, got %d lines", len(lines))
	}
	if mdText(lines[0].spans) == "" || lines[1].indent <= 0 {
		t.Fatalf("no hanging indent: %+v", lines)
	}
	prefix := TextWidth("- ", 1, f)
	if lines[1].indent != prefix {
		t.Fatalf("continuation indent = %d, want prefix width %d", lines[1].indent, prefix)
	}
}

func mdLineWidth(l mdLine, f viewer.Font) int {
	w := l.indent
	for _, s := range l.spans {
		w += TextWidth(s.text, l.scale, f)
		if s.kind == mdBold {
			w++
		}
	}
	return w
}

func TestMarkdownLinesFitTheirWidth(t *testing.T) {
	texts := []string{
		"## Release v1.2.3\n\n**Breaking:** drop `legacy` mode, see https://example.com/migrate.\n\n- alpha one\n- beta two with a very long tail that must wrap somewhere\n\n1. first\n2. second\n\n```\nsome --flag value\n```\n\n---\n\n> quoted remark\n\n| a | b |\n|---|---|\n| x | y |",
		"one two three four five six seven eight nine ten eleven twelve",
		"averylongwordthatcannotfitonalinebyitselfandmustbreakmidword",
	}
	for _, f := range []viewer.Font{viewer.RetroFont, viewer.CleanFont} {
		for _, width := range []int{400, 180, 90} {
			for _, scale := range []int{1, 2} {
				lines := layoutMarkdown(texts[0]+texts[1]+texts[2], scale, f, width)
				for _, l := range lines {
					if l.blank || l.rule {
						continue
					}
					if w := mdLineWidth(l, f); w > width {
						t.Fatalf("line %q is %d px wide in %d px (scale %d)", mdText(l.spans), w, width, scale)
					}
				}
				if h := MarkdownHeight(texts[0], scale, f, width); h <= 0 {
					t.Fatalf("no height for markdown at width %d scale %d", width, scale)
				}
			}
		}
	}
}

func TestMarkdownRendersThroughHarness(t *testing.T) {
	h := newHarness(400, 600)
	const text = "## v1.2.3\n\n- **Fix:** the `widget` — see https://example.com.\n\n---\n"
	r := Rect{X: 10, Y: 10, W: 380, H: 580}
	h.ctx.Begin(h.ctx.Canvas, h.in)
	Markdown(h.ctx, r, text, MarkdownStyle{Scale: 1})
	h.ctx.End()
}
