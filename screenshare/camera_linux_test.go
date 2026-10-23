package screenshare

import (
	"slices"
	"strings"
	"testing"
)

// GStreamer lays the camera over the screen with a compositor in front of the encoder.
func TestCameraOverlayGstreamer(t *testing.T) {
	opt := PresetByIndex(1).Options("desktop")
	opt.Camera = CameraPrefix + "/dev/video0"
	_, args, err := buildGstreamerCommand([]string{"videotestsrc", "is-live=true"}, "udp://127.0.0.1:5999?pkt_size=1128", opt, false)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !slices.Equal(args[:3], []string{"-q", "compositor", "name=pip"}) {
		t.Fatalf("compositor first: %q", joined)
	}
	for _, want := range []string{"udpsink host=127.0.0.1 port=5999 sync=false videotestsrc", "! pip.sink_0 v4l2src device=/dev/video0", "! pip.sink_1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
}
