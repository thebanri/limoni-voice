package screenshare

import "testing"

func TestParseMacHelperList(t *testing.T) {
	out := "PERMISSION|screen\nSCREEN|1|Display 1 (3024x1964)\nSCREEN|5|Display 2 (2560x1440)\n" +
		"WIN|42|Safari - Docs\nWIN|43|Safari - Docs\nWIN|44|Window Server - Item-0\nWIN|77|Terminal - zsh\n"
	targets, perm := parseMacHelperList(out)
	if !perm {
		t.Fatal("missing permission not reported")
	}
	want := []string{"desktop", "display:1", "display:5", "42", "77"}
	if len(targets) != len(want) {
		t.Fatalf("got %+v", targets)
	}
	for i, id := range want {
		if targets[i].ID != id {
			t.Fatalf("target %d = %q, want %q (%+v)", i, targets[i].ID, id, targets)
		}
	}
	if MacWindowTarget("display:5") || !MacWindowTarget("42") {
		t.Fatal("display targets must not count as windows")
	}

	// One display: only the "Entire Screen" entry; no output at all still offers the screen.
	targets, perm = parseMacHelperList("SCREEN|1|Display 1 (1440x900)\n")
	if perm || len(targets) != 1 || targets[0].ID != "desktop" {
		t.Fatalf("single display: %+v %v", targets, perm)
	}
	if targets, _ := parseMacHelperList(""); len(targets) != 1 {
		t.Fatalf("empty output: %+v", targets)
	}
}
