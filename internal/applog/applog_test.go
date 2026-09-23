package applog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDroppedWhenClosed(t *testing.T) {
	Close()
	Print("nobody hears this") // must not panic
	if file != nil {
		t.Fatal("log open without Open")
	}
}

func TestWritesAndRotates(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "app.log")
	if err := Open(p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Close)

	Print("first line")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), " first line\n") {
		t.Fatalf("unexpected log contents %q", data)
	}

	big := strings.Repeat("x", 64<<10)
	for i := 0; i < MaxSize/len(big)+2; i++ {
		Print(big)
	}
	if st, err := os.Stat(p); err != nil || st.Size() > MaxSize {
		t.Fatalf("log not rotated: %v %v", st, err)
	}
	backup, err := os.ReadFile(p + ".1")
	if err != nil {
		t.Fatalf("no backup after rotation: %v", err)
	}
	if !strings.Contains(string(backup), "first line") {
		t.Fatal("backup lost the older lines")
	}
}

func TestLogFileOverride(t *testing.T) {
	t.Setenv("LIMONI_LOG_FILE", "/tmp/x.log")
	if DefaultPath() != "/tmp/x.log" {
		t.Fatal("LIMONI_LOG_FILE ignored")
	}
}
