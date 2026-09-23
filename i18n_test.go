package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/thebanri/limoni-voice/internal/audioio"
	"github.com/thebanri/limoni-voice/internal/i18n"
	"github.com/thebanri/limoni-voice/screenshare"
)

// Every message the UI passes to T or Tf has a Turkish translation.
func TestEveryUIMessageIsTranslated(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			fn, ok := call.Fun.(*ast.Ident)
			if !ok || (fn.Name != "T" && fn.Name != "Tf") {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			msg, _ := strconv.Unquote(lit.Value)
			checked++
			if !i18n.Has(i18n.Turkish, msg) {
				t.Errorf("%s: no Turkish translation for %q", fset.Position(lit.Pos()), msg)
			}
			return true
		})
	}
	if checked < 300 {
		t.Fatalf("only %d messages found; is the scan broken?", checked)
	}
}

// Labels drawn in fixed columns (the next widget starts at a set offset) must not grow.
func TestFixedColumnLabelsFit(t *testing.T) {
	t.Cleanup(func() { i18n.Set(i18n.English) })
	for _, label := range []string{
		"Status: ", "Microphone [1]:", "Output Dev [2]:", "Noise Filter [N]:", "Input Mode [P]:",
		"Theme [T]:", "Mic Volume:    [ %3d%% ]", "Speaker Vol:   [ %3d%% ]", "Sensitivity:   [ %3d%% ]",
	} {
		i18n.Set(i18n.Turkish)
		tr := T(label)
		i18n.Set(i18n.English)
		if len([]rune(tr)) > len([]rune(label)) {
			t.Errorf("%q is %d columns in Turkish (%q), more than the %d it has", label, len([]rune(tr)), tr, len([]rune(label)))
		}
	}
}

// Network log lines, formatted in English, reach the room in Turkish.
func TestRoomLogIsTranslated(t *testing.T) {
	i18n.Set(i18n.Turkish)
	t.Cleanup(func() { i18n.Set(i18n.English) })
	for in, want := range map[string]string{
		"[-] Bob left the room.":                      "[-] Bob odadan ayrıldı.",
		"Noise Filter: HIGH":                          "Gürültü Filtresi: YÜKSEK",
		"📥 Incoming file from Ada: notes.txt":         "📥 Ada kişisinden gelen dosya: notes.txt",
		"[FILE] Downloading 'a.txt' completed (1 KB)": "[FILE] İndirme 'a.txt' tamamlandı (1 KB)",
	} {
		if got := tr(in); got != want {
			t.Errorf("tr(%q) = %q, want %q", in, got, want)
		}
	}
}

// Permission hints come from other packages as constants; they are translated too.
func TestPermissionHintsAreTranslated(t *testing.T) {
	for _, msg := range []string{audioio.MicPermissionHint, "[WARN] " + audioio.MicPermissionHint, screenshare.MacScreenPermissionHint} {
		if !i18n.Has(i18n.Turkish, msg) {
			t.Errorf("no Turkish translation for %q", msg)
		}
	}
}
