package main

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// A program opened for the user is reaped when it exits, not left as a zombie: xdg-open
// hands the file to an editor and quits at once, and 13 of them were found <defunct>.
func TestStartDetachedLeavesNoZombie(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	cmd := exec.Command("true")
	if err := startDetached(cmd); err != nil {
		t.Fatal(err)
	}
	stat := "/proc/" + strconv.Itoa(cmd.Process.Pid) + "/stat"
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(stat); os.IsNotExist(err) {
			return // exited and reaped
		}
		time.Sleep(10 * time.Millisecond)
	}
	b, _ := os.ReadFile(stat)
	t.Fatalf("process still there: %s", b)
}
