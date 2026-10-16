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

// A playing program is named from its stream or, for native PipeWire clients, its client;
// Limoni Voice itself is never offered.
func TestStreamApp(t *testing.T) {
	props := func(kv ...string) proto.PropList {
		p := proto.PropList{}
		for i := 0; i+1 < len(kv); i += 2 {
			p[kv[i]] = proto.PropListString(kv[i+1])
		}
		return p
	}
	if app, ok := streamApp(props("application.process.id", "4242", "application.name", "Haruna"), nil); !ok || app != (App{PID: 4242, Name: "Haruna"}) {
		t.Errorf("stream properties: got %+v %v", app, ok)
	}
	if app, ok := streamApp(props(), props("application.process.id", "77", "application.process.binary", "pikap")); !ok || app != (App{PID: 77, Name: "pikap"}) {
		t.Errorf("client properties: got %+v %v", app, ok)
	}
	if _, ok := streamApp(props("application.name", "Limoni Voice", "application.process.id", "99"), nil); ok {
		t.Error("Limoni Voice's own stream was offered")
	}
}
