package screenshare

import (
	"os/exec"
	"testing"
)

// gstPipewireElements are the GStreamer elements buildGstreamerPipewireCommand uses, with the
// package that provides each (the encoder is checked separately).
var gstPipewireElements = []struct {
	name string
	dep  linuxDep
}{
	{"pipewiresrc", depGstPipewire},
	{"queue", depGstTools},
	{"fdsink", depGstTools},
	{"videoconvert", depGstBase},
	{"videoscale", depGstBase},
	{"videorate", depGstBase},
	{"udpsink", depGstGood},
	{"h264parse", depGstBad},
	{"mpegtsmux", depGstBad},
}

// gstPipewireDepsError checks that the PipeWire capture pipeline (Wayland screens and windows)
// can run: gst-launch-1.0 and every element it uses, including an H.264 encoder.
func gstPipewireDepsError() error {
	if testing.Testing() {
		return nil
	}
	return gstPipewireDeps(true)
}

// gstPipewireDeps is gstPipewireDepsError; checkEncoder false skips the encoder probe, which
// runs test pipelines and takes a moment on first use.
func gstPipewireDeps(checkEncoder bool) error {
	if _, err := FindExecutable("gst-launch-1.0"); err != nil {
		missing := []string{"gst-launch-1.0"}
		deps := []linuxDep{depGstTools, depGstPipewire, depGstBase, depGstGood, depGstBad, depGstUgly}
		return newMissingDepsError(false, missing, deps)
	}
	inspect, err := FindExecutable("gst-inspect-1.0")
	if err != nil {
		return nil // same package as gst-launch-1.0; without it the elements cannot be checked
	}
	has := func(element string) bool { return exec.Command(inspect, "--exists", element).Run() == nil }

	var missing []string
	var deps []linuxDep
	for _, e := range gstPipewireElements {
		if !has(e.name) {
			missing = append(missing, "GStreamer "+e.name)
			deps = append(deps, e.dep)
		}
	}
	// getBestLinuxEncoder falls back to x264enc when no hardware encoder works.
	if checkEncoder && getBestLinuxEncoder() == "x264enc" && !has("x264enc") {
		missing = append(missing, "GStreamer x264enc")
		deps = append(deps, depGstUgly)
	}
	if len(missing) == 0 {
		return nil
	}
	return newMissingDepsError(false, missing, deps)
}
