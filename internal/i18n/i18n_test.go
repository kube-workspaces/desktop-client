// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

package i18n

import (
	"strings"
	"testing"
)

func TestTablesAreComplete(t *testing.T) {
	if len(english) == 0 {
		t.Fatal("the English table is empty")
	}
	for _, k := range EnglishKeys() {
		if english[k] == "" {
			t.Errorf("key %q has an empty message", k)
		}
		// A stray or mistyped verb formats as %!s(MISSING) in the window;
		// only the verbs the call sites pass may appear.
		for _, bad := range []string{"%!", "%T", "%p", "%x"} {
			if strings.Contains(english[k], bad) {
				t.Errorf("key %q contains %q", k, bad)
			}
		}
	}
}

func TestLookupFallsBackToEnglishThenKey(t *testing.T) {
	defer SetLocale("en")
	SetLocale("en")
	if got := Get("app.name"); got != "Kube Workspaces" {
		t.Fatalf("Get(app.name) = %q", got)
	}
	// An unknown locale is English, not blank.
	SetLocale("zz")
	if got := Get("app.name"); got != "Kube Workspaces" {
		t.Fatalf("unknown locale gave %q", got)
	}
	// An unknown key is the key itself: debuggable in the window, and the
	// pseudo-locale below still wraps it.
	if got := Get("no.such.key"); got != "no.such.key" {
		t.Fatalf("unknown key gave %q", got)
	}
	if got := Sprintf("workspaces.noMatch", "team/vm-a"); !strings.Contains(got, "team/vm-a") {
		t.Fatalf("Sprintf dropped its argument: %q", got)
	}
}

func TestPseudoLocaleExpandsAndWraps(t *testing.T) {
	defer SetLocale("en")
	SetLocale("xx")
	got := Get("server.connect")
	if !strings.HasPrefix(got, "[[") || !strings.HasSuffix(got, "]]") {
		t.Fatalf("pseudo-locale did not wrap: %q", got)
	}
	if len([]rune(got)) <= len([]rune("Connect")) {
		t.Fatalf("pseudo-locale did not expand: %q", got)
	}
	// Pure ASCII, so it renders in every face including the bitmap ones.
	for _, r := range got {
		if r > 127 {
			t.Fatalf("pseudo-locale left ASCII: %q", got)
		}
	}
	// Empty stays empty: no control buys brackets.
	SetLocale("en")
	if got := Get("missing"); got != "missing" {
		t.Fatalf("fallback broke: %q", got)
	}
}

func TestSystemLocaleParsing(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{
		{nil, "en"},
		{map[string]string{"LANG": "de_DE.UTF-8"}, "de"},
		{map[string]string{"LANG": "fr-FR"}, "fr"},
		{map[string]string{"LANG": "C"}, "en"},
		{map[string]string{"LANG": "POSIX"}, "en"},
		{map[string]string{"LC_ALL": "ja_JP.UTF-8", "LANG": "de_DE.UTF-8"}, "ja"},
		{map[string]string{"LANGUAGE": "es:de", "LANG": "C"}, "es"},
	} {
		t.Setenv("LC_ALL", "")
		t.Setenv("LANG", "")
		t.Setenv("LANGUAGE", "")
		for k, v := range tc.env {
			t.Setenv(k, v)
		}
		if got := SystemLocale(); got != tc.want {
			t.Errorf("env %v: SystemLocale = %q, want %q", tc.env, got, tc.want)
		}
	}
}
