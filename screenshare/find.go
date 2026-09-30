package screenshare

import "github.com/thebanri/limoni-voice/internal/toolpath"

// FindExecutable finds an external program; see toolpath.Find.
func FindExecutable(name string) (string, error) { return toolpath.Find(name) }
