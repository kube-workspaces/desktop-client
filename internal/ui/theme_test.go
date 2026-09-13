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
	for _, mode := range []Mode{ModeDark, ModeLight} {
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
}
