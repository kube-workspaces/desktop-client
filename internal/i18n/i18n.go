// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Package i18n is the client's message catalog: every sentence the window
// shows lives here in English, looked up by a stable key, so a locale is
// content rather than engineering.
//
// Only English ships today, which makes this look like ceremony. It is not:
// the failure it prevents is user-visible strings scattered across the screen
// code where no translator — and no consistency pass — can ever find them.
// Adding a locale later means adding one table file and one entry in tables,
// not touching a single call site.
//
// Lookup never fails visibly: an unknown locale falls back to English, and an
// unknown key returns the key itself, so a missing entry is a debuggable
// label rather than a blank control. The "xx" locale is the pseudo-locale the
// tests use to prove translated-length strings still fit: every message
// wrapped and padded, in pure ASCII so it renders in every face.
package i18n

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

var (
	mu      sync.RWMutex
	current = "en"
	tables  = map[string]map[string]string{
		"en": english,
		// The pseudo-locale carries no messages of its own: every key
		// falls back to English and the expand step wraps it.
		"xx": {},
	}
)

// Locale reports the active locale tag ("en", or "xx" in tests).
func Locale() string {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// SetLocale selects the message locale. An empty tag follows the system, an
// unknown tag falls back to English. It is called once at startup (and by
// tests); the shell reads no locale while running.
func SetLocale(tag string) {
	if tag == "" {
		tag = SystemLocale()
	}
	tag = normalizeTag(tag)
	mu.Lock()
	defer mu.Unlock()
	if _, ok := tables[tag]; !ok {
		tag = "en"
	}
	current = tag
}

// Get returns the message for key in the active locale, falling back to
// English and then to the key itself. It never returns "".
func Get(key string) string {
	mu.RLock()
	t, ok := tables[current]
	xx := current == "xx"
	mu.RUnlock()
	if ok {
		if s, ok := t[key]; ok {
			return expand(s, xx)
		}
	}
	if s, ok := english[key]; ok {
		return expand(s, xx)
	}
	return key
}

// Sprintf returns the formatted message for key, with the same fallback as
// [Get]. Verbs stay in the table so translators can reorder them; the call
// sites pass only values.
func Sprintf(key string, args ...any) string {
	return fmt.Sprintf(Get(key), args...)
}

// expand applies the pseudo-locale transform: English passes through, "xx"
// wraps the message in brackets and pads it ~40% longer to simulate
// translated text without leaving ASCII.
func expand(s string, xx bool) string {
	if !xx || s == "" {
		return s
	}
	return "[[" + s + strings.Repeat("~", len([]rune(s))/3+1) + "]]"
}

// normalizeTag reduces "de_DE.UTF-8" and "de-DE" to "de".
func normalizeTag(tag string) string {
	tag = strings.TrimSpace(tag)
	if i := strings.IndexAny(tag, "_.@"); i >= 0 {
		tag = tag[:i]
	}
	if i := strings.Index(tag, "-"); i >= 0 {
		tag = tag[:i]
	}
	return strings.ToLower(tag)
}

// SystemLocale detects the locale from the environment: LC_ALL first, then
// LANG, then the first entry of LANGUAGE. "C", "POSIX" and anything
// unparseable mean English.
func SystemLocale() string {
	if tag := normalizeTag(os.Getenv("LC_ALL")); usableTag(tag) {
		return tag
	}
	if lang := os.Getenv("LANGUAGE"); lang != "" {
		// A colon-separated priority list ("es:de"); the first entry wins.
		if i := strings.Index(lang, ":"); i >= 0 {
			lang = lang[:i]
		}
		if tag := normalizeTag(lang); usableTag(tag) {
			return tag
		}
	}
	if tag := normalizeTag(os.Getenv("LANG")); usableTag(tag) {
		return tag
	}
	return "en"
}

// usableTag reports whether tag names a real locale rather than the absence
// of one.
func usableTag(tag string) bool {
	switch tag {
	case "", "c", "posix":
		return false
	}
	return true
}

// EnglishKeys lists every key in the English table, for the completeness
// tests.
func EnglishKeys() []string {
	keys := make([]string, 0, len(english))
	for k := range english {
		keys = append(keys, k)
	}
	return keys
}
