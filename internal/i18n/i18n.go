// Package i18n translates the user interface.
//
// English is the source language: every message is written in English in the code and that
// text is its key. Another language supplies a catalog from English to its own text. A
// missing entry falls back to English, so an untranslated message is never lost.
//
// Messages written with fmt verbs are translated two ways. T and Tf look the format up before
// it is filled in, which is what the UI code does. Translate takes a message that was
// already formatted in English (a log line from the network layer, which keeps writing its
// own log file in English) and matches it against the catalog's formats, carrying the
// filled-in values over into the translation.
package i18n

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Lang is a user interface language.
type Lang string

const (
	English Lang = "en"
	Turkish Lang = "tr"
)

// Languages lists the supported languages in the order the settings cycle through them.
var Languages = []Lang{English, Turkish}

// Name returns the language's name in that language.
func (l Lang) Name() string {
	switch l {
	case Turkish:
		return "Türkçe"
	default:
		return "English"
	}
}

// Parse reads a language code such as "tr", "TR" or "tr_TR.UTF-8". ok is false for
// languages without a translation.
func Parse(s string) (Lang, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexAny(s, "_-.@"); i >= 0 {
		s = s[:i]
	}
	for _, l := range Languages {
		if string(l) == s {
			return l, true
		}
	}
	return English, false
}

var current atomic.Value // Lang

func init() { current.Store(English) }

// Current returns the active language.
func Current() Lang { return current.Load().(Lang) }

// Set switches the active language.
func Set(l Lang) {
	if _, ok := Parse(string(l)); !ok {
		l = English
	}
	current.Store(l)
}

// Next returns the language after l in Languages.
func Next(l Lang) Lang {
	for i, x := range Languages {
		if x == l {
			return Languages[(i+1)%len(Languages)]
		}
	}
	return English
}

var catalogs = map[Lang]map[string]string{
	Turkish: turkish,
}

// T translates a fixed message.
func T(msg string) string {
	cat := catalogs[Current()]
	if cat == nil {
		return msg
	}
	if tr, ok := cat[msg]; ok {
		return tr
	}
	return msg
}

// Tf translates a format and fills it in.
func Tf(format string, args ...any) string {
	return fmt.Sprintf(T(format), args...)
}

// Translate translates a message that was formatted in English. Leading and trailing
// spaces are kept. Messages without a translation come back unchanged.
func Translate(msg string) string {
	lang := Current()
	cat := catalogs[lang]
	if cat == nil || msg == "" {
		return msg
	}
	core := strings.TrimSpace(msg)
	if core == "" {
		return msg
	}
	lead := msg[:strings.Index(msg, core)]
	trail := msg[len(lead)+len(core):]
	return lead + translateCore(lang, cat, core) + trail
}

func translateCore(lang Lang, cat map[string]string, msg string) string {
	if tr, ok := cat[msg]; ok {
		return tr
	}
	cache := cacheFor(lang)
	if tr, ok := cache.get(msg); ok {
		return tr
	}
	out := msg
	for _, p := range patternsFor(lang) {
		if m := p.re.FindStringSubmatch(msg); m != nil {
			out = fill(p.target, m[1:])
			break
		}
	}
	cache.put(msg, out)
	return out
}

// pattern matches messages produced by one catalog format.
type pattern struct {
	re     *regexp.Regexp
	target string
	fixed  int // literal characters, to try the most specific formats first
}

var (
	patternsMu sync.Mutex
	patterns   = map[Lang][]pattern{}
)

var verbRe = regexp.MustCompile(`%(\[\d+\])?[-+# 0]*\d*(\.\d+)?[vsdqxXfegtc%]`)

func patternsFor(lang Lang) []pattern {
	patternsMu.Lock()
	defer patternsMu.Unlock()
	if ps, ok := patterns[lang]; ok {
		return ps
	}
	var ps []pattern
	for src, target := range catalogs[lang] {
		locs := verbRe.FindAllStringIndex(src, -1)
		verbs := 0
		for _, l := range locs {
			if src[l[1]-1] != '%' {
				verbs++
			}
		}
		if verbs == 0 {
			continue
		}
		var b strings.Builder
		b.WriteString("^")
		last, fixed := 0, 0
		for _, l := range locs {
			lit := src[last:l[0]]
			fixed += len(lit)
			b.WriteString(regexp.QuoteMeta(lit))
			if src[l[1]-1] == '%' {
				b.WriteString("%")
				fixed++
			} else {
				b.WriteString("(.*?)")
			}
			last = l[1]
		}
		fixed += len(src[last:])
		b.WriteString(regexp.QuoteMeta(src[last:]))
		b.WriteString("$")
		re, err := regexp.Compile(b.String())
		if err != nil {
			continue
		}
		ps = append(ps, pattern{re: re, target: target, fixed: fixed})
	}
	// Longest literal text first, then the pattern text itself, so the choice is stable.
	sortPatterns(ps)
	patterns[lang] = ps
	return ps
}

func sortPatterns(ps []pattern) {
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0 && less(ps[j], ps[j-1]); j-- {
			ps[j], ps[j-1] = ps[j-1], ps[j]
		}
	}
}

func less(a, b pattern) bool {
	if a.fixed != b.fixed {
		return a.fixed > b.fixed
	}
	return a.re.String() < b.re.String()
}

// fill replaces the verbs of target with values, in order or by explicit %[n] index. A value
// that is itself a catalog entry (a mode name, a status) is translated too.
func fill(target string, values []string) string {
	cat := catalogs[Current()]
	for i, v := range values {
		if t, ok := cat[v]; ok {
			values[i] = t
		}
	}
	var b strings.Builder
	next := 0
	last := 0
	for _, l := range verbRe.FindAllStringSubmatchIndex(target, -1) {
		b.WriteString(target[last:l[0]])
		last = l[1]
		verb := target[l[0]:l[1]]
		if strings.HasSuffix(verb, "%") && len(verb) == 2 {
			b.WriteString("%")
			continue
		}
		idx := next
		if l[2] >= 0 {
			if n, err := strconv.Atoi(target[l[2]+1 : l[3]-1]); err == nil {
				idx = n - 1
			}
		}
		next = idx + 1
		if idx >= 0 && idx < len(values) {
			b.WriteString(values[idx])
		}
	}
	b.WriteString(target[last:])
	return b.String()
}

// cache remembers translated messages; the UI redraws the same lines many times a second.
type cache struct {
	mu sync.Mutex
	m  map[string]string
}

const cacheLimit = 4096

var (
	cachesMu sync.Mutex
	caches   = map[Lang]*cache{}
)

func cacheFor(lang Lang) *cache {
	cachesMu.Lock()
	defer cachesMu.Unlock()
	c := caches[lang]
	if c == nil {
		c = &cache{m: make(map[string]string)}
		caches[lang] = c
	}
	return c
}

func (c *cache) get(k string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[k]
	return v, ok
}

func (c *cache) put(k, v string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= cacheLimit {
		clear(c.m)
	}
	c.m[k] = v
}

// Has reports whether lang's catalog translates msg.
func Has(lang Lang, msg string) bool {
	_, ok := catalogs[lang][msg]
	return ok
}
