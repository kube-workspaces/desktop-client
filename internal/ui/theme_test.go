// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"testing"

	"github.com/kube-workspaces/desktop-client/internal/viewer"
)

func TestStylesAndModesRoundTrip(t *testing.T) {
	for _, style := range []Style{StyleBubbly, StyleRetro, StyleClean} {
		if got, ok := ParseStyle(style.String()); !ok || got != style {
			t.Fatalf("ParseStyle(%q) = %v, %t, want %v", style.String(), got, ok, style)
		}
	}
	for _, mode := range []Mode{ModeDark, ModeLight, ModeSystem} {
		if got, ok := ParseMode(mode.String()); !ok || got != mode {
			t.Fatalf("ParseMode(%q) = %v, %t, want %v", mode.String(), got, ok, mode)
		}
	}
	if _, ok := ParseStyle(""); ok {
		t.Fatal("an empty style parsed as a known style")
	}
	if _, ok := ParseMode("sepia"); ok {
		t.Fatal("an unknown mode parsed as a known mode")
	}
}

func TestParseStyleAcceptsTheLegacyModernName(t *testing.T) {
	if got, ok := ParseStyle("modern"); !ok || got != StyleBubbly {
		t.Fatalf("ParseStyle(\"modern\") = %v, %t, want the bubbly style", got, ok)
	}
	if got := StyleBubbly.String(); got != "bubbly" {
		t.Fatalf("StyleBubbly.String() = %q, want the canonical persisted name", got)
	}
}

func TestRetroIsTheOriginalTheme(t *testing.T) {
	got := ThemeFor(StyleRetro, ModeDark)
	if got.Body != 2 || got.Title != 3 || got.Small != 1 {
		t.Fatalf("retro scales are %d/%d/%d, want 2/3/1", got.Body, got.Title, got.Small)
	}
	if got.Radius != 6 || got.ControlHeight != 34 || got.RowHeight != 46 {
		t.Fatalf("retro geometry differs from v0.1.0: radius=%d control=%d row=%d",
			got.Radius, got.ControlHeight, got.RowHeight)
	}
	if got.Background != rgb(0x14, 0x16, 0x1a) {
		t.Fatalf("retro background = %v, want the v0.1.0 dark background", got.Background)
	}
}

func TestBubblyIsTheDefaultAndIsCrisp(t *testing.T) {
	got := DefaultTheme()
	if got.Body != 2 || got.Title != 3 {
		t.Fatalf("the default (bubbly) scales are %d/%d, want 2/3", got.Body, got.Title)
	}
	if got.Radius != 2 {
		t.Fatalf("the default corner radius is %d, want the small bubbly one", got.Radius)
	}
	// Style changes typography only: the same mode must keep the same ink.
	if ThemeFor(StyleBubbly, ModeLight).Background != ThemeFor(StyleRetro, ModeLight).Background {
		t.Fatal("bubbly changed the light palette; style and colour must stay orthogonal")
	}
}

func TestStylesUseTheirOwnTypeface(t *testing.T) {
	glyph := func(f viewer.Font) viewer.Raster { return f.Glyph('a', 1) }
	if glyph(DefaultTheme().Font) != glyph(viewer.BubblyFont) {
		t.Fatal("the bubbly theme does not use the bubbly typeface")
	}
	if glyph(ThemeFor(StyleRetro, ModeDark).Font) != glyph(viewer.RetroFont) {
		t.Fatal("the retro theme does not use the retro typeface")
	}
	if glyph(ThemeFor(StyleClean, ModeDark).Font) != glyph(viewer.CleanFont) {
		t.Fatal("the clean theme does not use the clean typeface")
	}
	if DefaultTheme().Font.Glyph('a', 1) == ThemeFor(StyleRetro, ModeDark).Font.Glyph('a', 1) {
		t.Fatal("the styles render 'a' identically; the typefaces must differ")
	}
	if DefaultTheme().Font.Glyph('a', 2) == viewer.CleanFont.Glyph('a', 2) {
		t.Fatal("the clean face renders 'a' identically to the bubbly face")
	}
	// The two bitmap faces share the same cell; the clean face is proportional
	// (per-glyph advances, no fixed cell), and layout must see that.
	if DefaultTheme().Font.GlyphW != viewer.RetroFont.GlyphW || DefaultTheme().Font.GlyphH != viewer.RetroFont.GlyphH {
		t.Fatal("the bitmap faces disagree on the 5x8 cell")
	}
	if viewer.CleanFont.Glyph('a', 1) == viewer.RetroFont.Glyph('a', 1) {
		t.Fatal("the clean face draws 'a' identically to the 5x8 face")
	}
	if viewer.CleanFont.Advance == nil {
		t.Fatal("the clean face is not proportional")
	}
}

func TestLightModeIsDistinctFromDark(t *testing.T) {
	dark := ThemeFor(StyleBubbly, ModeDark)
	light := ThemeFor(StyleBubbly, ModeLight)
	if light.Background == dark.Background || light.Text == dark.Text {
		t.Fatal("light mode reuses the dark palette")
	}
	if light.Background == Transparent {
		t.Fatal("light mode has no background")
	}
	// ModeSystem is a resolution instruction, not a palette. A caller that
	// fails to resolve it draws the historic default rather than a made-up
	// scheme — the shell resolves the mode before it ever reaches ThemeFor.
	system := ThemeFor(StyleBubbly, ModeSystem)
	if system.Background != dark.Background {
		t.Fatal("an unresolved system mode must fall back to the default dark scheme")
	}
}

func TestQuantizeUIScale(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want float64
	}{
		{0, 1},
		{0.5, 1},
		{1, 1},
		{1.25, 1.5},
		{1.5, 1.5},
		{1.75, 2},
		{2, 2},
		{2.5, 2.5},
		{3, 3},
		{4, 3},
	} {
		if got := QuantizeUIScale(tc.in); got != tc.want {
			t.Errorf("QuantizeUIScale(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestScaledThemeDoublesTheBubblyMetrics(t *testing.T) {
	base := ThemeFor(StyleBubbly, ModeDark)
	got := base.Scaled(2)
	// Bubbly body/title/small 2/3/1, pad/gap/radius 14/8/2, control/row
	// 30/32, border/focus 1/1.
	if got.Body != 4 || got.Title != 6 || got.Small != 2 {
		t.Fatalf("scaled font scales are %d/%d/%d, want 4/6/2", got.Body, got.Title, got.Small)
	}
	if got.Pad != 28 || got.Gap != 16 || got.Radius != 4 {
		t.Fatalf("scaled insets are %d/%d/%d, want 28/16/4", got.Pad, got.Gap, got.Radius)
	}
	if got.ControlHeight != 60 || got.RowHeight != 64 {
		t.Fatalf("scaled controls are %d/%d, want 60/64", got.ControlHeight, got.RowHeight)
	}
	if got.BorderWidth != 2 || got.FocusWidth != 2 {
		t.Fatalf("scaled strokes are %d/%d, want 2/2", got.BorderWidth, got.FocusWidth)
	}
	// Scaling sizes, not the look: colours ride along unchanged, and so does
	// the typeface (compared by cell, since Font holds funcs that == cannot
	// touch).
	if got.Background != base.Background || got.Accent != base.Accent ||
		got.Font.GlyphW != base.Font.GlyphW || got.Font.GlyphH != base.Font.GlyphH {
		t.Fatal("scaling changed the palette or the typeface")
	}
	// And the base is untouched: Scaled copies.
	if base.Body != 2 || base.Pad != 14 {
		t.Fatal("Scaled modified the theme it was called on")
	}
}

func TestScaledThemeAtOrBelowOneIsIdentity(t *testing.T) {
	base := ThemeFor(StyleBubbly, ModeDark)
	for _, f := range []float64{1, 0.5, 0, -2} {
		got := base.Scaled(f)
		// Font holds func fields, so the structs cannot be compared with
		// ==; compare the metrics instead.
		if got.Body != base.Body || got.Title != base.Title || got.Small != base.Small ||
			got.Pad != base.Pad || got.Gap != base.Gap || got.Radius != base.Radius ||
			got.ControlHeight != base.ControlHeight || got.RowHeight != base.RowHeight {
			t.Fatalf("Scaled(%v) changed the theme", f)
		}
	}
}

func TestScaledThemeRoundsFractionalFactors(t *testing.T) {
	base := ThemeFor(StyleBubbly, ModeDark)
	got := base.Scaled(1.5)
	// Font scales stay whole: fractional bitmap glyphs are mush.
	if got.Body != 3 || got.Title != 5 || got.Small != 2 {
		t.Fatalf("1.5x font scales are %d/%d/%d, want 3/5/2", got.Body, got.Title, got.Small)
	}
	if got.ControlHeight != 45 {
		t.Fatalf("1.5x control height is %d, want 45", got.ControlHeight)
	}
}
