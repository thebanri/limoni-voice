//go:build !windows && !darwin

package sysaudio

import (
	"os"
	"strconv"
	"testing"

	"github.com/jfreymuth/pulse/proto"
)

// Limoni Voice's own playback is never shared: its process, a player it started, and a
// stream carrying its name; other programs are.
func TestIsSelf(t *testing.T) {
	props := func(kv ...string) proto.PropList {
		p := proto.PropList{}
		for i := 0; i+1 < len(kv); i += 2 {
			p[kv[i]] = proto.PropListString(kv[i+1])
		}
		return p
	}
	cases := []struct {
		name  string
		props proto.PropList
		want  bool
	}{
		{"own process", props("application.process.id", strconv.Itoa(os.Getpid())), true},
		{"own name", props("application.name", "Limoni Voice"), true},
		{"other program", props("application.process.id", "1", "application.name", "Firefox"), false},
		{"no properties", props(), false},
	}
	for _, c := range cases {
		if got := isSelf(c.props); got != c.want {
			t.Errorf("%s: isSelf = %v, want %v", c.name, got, c.want)
		}
	}
}
