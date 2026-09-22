package notify

import "testing"

func TestClean(t *testing.T) {
	cases := map[string]string{
		"hello":                    "hello",
		"line1\nline2\tx":          "line1 line2 x",
		"bell\x07 and \x1b[31mred": "bell and [31mred",
		"  spaced   out  ":         "spaced out",
		"sep arator":               "separator",
	}
	for in, want := range cases {
		if got := clean(in, 80); got != want {
			t.Errorf("clean(%q) = %q, want %q", in, got, want)
		}
	}
	if got := clean("abcdefghij", 5); got != "abcd…" {
		t.Errorf("truncation: %q", got)
	}
	if got := clean("çğüşöı🙂🙂🙂", 4); got != "çğü…" {
		t.Errorf("rune-safe truncation: %q", got)
	}
}

func TestEscapeMarkup(t *testing.T) {
	if got := escapeMarkup(`<b>hi</b> & "you"`); got != `&lt;b&gt;hi&lt;/b&gt; &amp; "you"` {
		t.Errorf("escapeMarkup = %q", got)
	}
}
