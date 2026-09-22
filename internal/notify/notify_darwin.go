//go:build darwin

package notify

import "os/exec"

func send(title, body string) error {
	// Text is passed as arguments, never interpolated into the AppleScript source.
	return exec.Command("osascript",
		"-e", "on run argv",
		"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
		"-e", "end run",
		title, body).Run()
}
