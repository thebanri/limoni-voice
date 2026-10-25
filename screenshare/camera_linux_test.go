package screenshare

import (
	"slices"
	"strings"
	"testing"
)

// GStreamer lays the camera over the screen with a compositor in front of the encoder.
func TestCameraOverlayGstreamer(t *testing.T) {
	caps := "video/x-raw,width=1280,height=720,framerate=30/1,format=I420"
	args := []string{"-q", "videotestsrc", "is-live=true", "!", "videoscale", "!", caps, "!", "x264enc", "!", "udpsink", "port=5999"}
	got := withGstCameraOverlay(args, caps, CameraPrefix+"/dev/video0", 1280, 720)
	joined := strings.Join(got, " ")
	if !slices.Equal(got[:3], []string{"-q", "compositor", "name=pip"}) {
		t.Fatalf("compositor first: %q", joined)
	}
	for _, want := range []string{
		"sink_1::xpos=1024 sink_1::ypos=524",
		"! videoconvert ! " + caps + " ! x264enc ! udpsink port=5999 videotestsrc",
		"! " + caps + " ! pip.sink_0 v4l2src device=/dev/video0",
		"! pip.sink_1",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
}
