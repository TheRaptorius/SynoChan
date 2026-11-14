// Package filter decides which threads a board may add, by words and phrases in their subject and text.
//
// Patterns match whole words, ignoring case and diacritics ("cafe" matches "Café"). A pattern may be a
// phrase of several words. "*" at the start or end of a word allows more letters there: "trap*" matches
// "trapped", "*trap" matches "mousetrap", "*trap*" matches both.
package filter

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// MaxPatterns and MaxPatternLength bound a list, so a pasted novel cannot slow every board check down.
const (
	MaxPatterns      = 500
	MaxPatternLength = 100
)

// Normalize lower-cases a string and removes diacritics.
func Normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if f, ok := fold[r]; ok {
			b.WriteString(f)
			continue
		}
		if unicode.Is(unicode.Mn, r) {
			continue // A combining mark left over from decomposed input.
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// words splits normalised text into words of letters and digits.
func words(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '*' })
}

type wordPattern struct {
	text           string
	prefix, suffix bool // "*" before or after: more letters allowed there.
}

func (p wordPattern) match(w string) bool {
	switch {
	case p.prefix && p.suffix:
		return strings.Contains(w, p.text)
	case p.prefix:
		return strings.HasSuffix(w, p.text)
	case p.suffix:
		return strings.HasPrefix(w, p.text)
	}
	return w == p.text
}

// Pattern is one compiled word or phrase.
type Pattern struct {
	Source string
	words  []wordPattern
}

// Compile parses a pattern. Patterns without any letter or digit (like "*") are rejected, as they would
// match everything.
func Compile(source string) (Pattern, error) {
	source = strings.Join(strings.Fields(source), " ")
	if len(source) > MaxPatternLength {
		return Pattern{}, fmt.Errorf("%q is longer than %d characters", source, MaxPatternLength)
	}
	p := Pattern{Source: source}
	for _, w := range words(Normalize(source)) {
		wp := wordPattern{prefix: strings.HasPrefix(w, "*"), suffix: strings.HasSuffix(w, "*")}
		wp.text = strings.Trim(w, "*")
		if strings.Contains(wp.text, "*") {
			return Pattern{}, fmt.Errorf("%q: \"*\" is only allowed at the start or end of a word", source)
		}
		if wp.text == "" {
			continue
		}
		p.words = append(p.words, wp)
	}
	if len(p.words) == 0 {
		return Pattern{}, fmt.Errorf("%q has no letters or digits to look for", source)
	}
	return p, nil
}

// matchWords reports whether the pattern's words occur consecutively in ws.
func (p Pattern) matchWords(ws []string) bool {
	for i := 0; i+len(p.words) <= len(ws); i++ {
		ok := true
		for j, wp := range p.words {
			if !wp.match(ws[i+j]) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// CleanList trims, drops empty entries and case/diacritic-insensitive duplicates, and validates every entry.
func CleanList(list []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, s := range list {
		s = strings.Join(strings.Fields(s), " ")
		if s == "" {
			continue
		}
		key := Normalize(s)
		if seen[key] {
			continue
		}
		if _, err := Compile(s); err != nil {
			return nil, err
		}
		seen[key] = true
		out = append(out, s)
	}
	if len(out) > MaxPatterns {
		return nil, fmt.Errorf("at most %d filter entries are allowed", MaxPatterns)
	}
	return out, nil
}

// Rules are the filters that apply to one board.
type Rules struct {
	Skip       []Pattern
	Only       []Pattern
	SkipSticky bool
}

// NewRules compiles a board's effective rules: skip patterns of both lists apply; the board's own "only"
// list replaces the global one when it has entries.
func NewRules(globalSkip, globalOnly, boardSkip, boardOnly []string, skipSticky bool) (Rules, error) {
	r := Rules{SkipSticky: skipSticky}
	only := globalOnly
	if len(boardOnly) > 0 {
		only = boardOnly
	}
	var errs []error
	for _, s := range append(append([]string(nil), globalSkip...), boardSkip...) {
		p, err := Compile(s)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		r.Skip = append(r.Skip, p)
	}
	for _, s := range only {
		p, err := Compile(s)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		r.Only = append(r.Only, p)
	}
	return r, errors.Join(errs...)
}

// Verdict tells whether a thread may be added, and why not.
type Verdict struct {
	Allowed bool
	Reason  string // For example `contains "trap"`, "sticky thread" or "matches none of the only-list".
}

// Check judges a thread by its subject and text (both may contain anything; they are normalised here).
func (r Rules) Check(subject, text string, sticky bool) Verdict {
	if sticky && r.SkipSticky {
		return Verdict{Reason: "sticky thread"}
	}
	ws := words(Normalize(subject + "\n" + text))
	for _, p := range r.Skip {
		if p.matchWords(ws) {
			return Verdict{Reason: fmt.Sprintf("contains %q", p.Source)}
		}
	}
	if len(r.Only) > 0 {
		for _, p := range r.Only {
			if p.matchWords(ws) {
				return Verdict{Allowed: true}
			}
		}
		return Verdict{Reason: "matches nothing in the only-list"}
	}
	return Verdict{Allowed: true}
}
