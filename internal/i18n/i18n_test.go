package i18n

import (
	"regexp"
	"strings"
	"testing"
)

func withLang(t *testing.T, l Lang) {
	t.Helper()
	prev := Current()
	Set(l)
	t.Cleanup(func() { Set(prev) })
}

func TestEnglishIsTheSource(t *testing.T) {
	withLang(t, English)
	if got := Tf("Volume for %s set to %d%%", "Bob", 80); got != "Volume for Bob set to 80%" {
		t.Fatal(got)
	}
	if got := Translate("[-] Bob left the room."); got != "[-] Bob left the room." {
		t.Fatal(got)
	}
}

func TestTurkishLookupAndFallback(t *testing.T) {
	withLang(t, Turkish)
	if got := T("Chat & room logs cleared"); got == "Chat & room logs cleared" {
		t.Fatal("known message not translated")
	}
	if got := T("no such message, keep it"); got != "no such message, keep it" {
		t.Fatal("unknown message changed:", got)
	}
}

func TestTranslateFormattedMessages(t *testing.T) {
	withLang(t, Turkish)
	catalogs[Turkish]["%s moved %d files to %s"] = "%[3]s klasörüne %[2]d dosya taşındı (%[1]s)"
	catalogs[Turkish]["Rate %d%% for %s"] = "%[2]s için oran %%%[1]d"
	patterns = map[Lang][]pattern{}
	caches = map[Lang]*cache{}
	t.Cleanup(func() {
		delete(catalogs[Turkish], "%s moved %d files to %s")
		delete(catalogs[Turkish], "Rate %d%% for %s")
		patterns = map[Lang][]pattern{}
		caches = map[Lang]*cache{}
	})

	if got := Translate("Ada moved 3 files to /tmp"); got != "/tmp klasörüne 3 dosya taşındı (Ada)" {
		t.Fatal(got)
	}
	if got := Translate("  Rate 40% for Bob "); got != "  Bob için oran %40 " {
		t.Fatalf("%q", got)
	}
}

// Every Turkish entry must keep the English entry's verbs, or filling it in breaks.
func TestTurkishCatalogKeepsVerbs(t *testing.T) {
	verbs := func(s string) []string {
		var out []string
		for _, v := range verbRe.FindAllString(s, -1) {
			if v != "%%" {
				out = append(out, regexp.MustCompile(`\[\d+\]`).ReplaceAllString(v, ""))
			}
		}
		return out
	}
	for en, tr := range turkish {
		ev, tv := verbs(en), verbs(tr)
		if len(ev) != len(tv) {
			t.Errorf("verb count differs:\n  %q\n  %q", en, tr)
			continue
		}
		if !strings.Contains(tr, "%[") {
			for i := range ev {
				if ev[i] != tv[i] {
					t.Errorf("verbs differ:\n  %q\n  %q", en, tr)
					break
				}
			}
		}
		if strings.TrimSpace(tr) == "" {
			t.Errorf("empty translation for %q", en)
		}
	}
}

func TestParse(t *testing.T) {
	for in, want := range map[string]Lang{"tr": Turkish, "tr_TR.UTF-8": Turkish, "EN": English, "en-US": English} {
		if got, ok := Parse(in); !ok || got != want {
			t.Errorf("Parse(%q) = %v %v", in, got, ok)
		}
	}
	if _, ok := Parse("de"); ok {
		t.Error("German has no catalog")
	}
}
