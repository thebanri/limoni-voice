package audioio

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// execBackend streams raw PCM through external tools (parec/pacat, pw-record/pw-play,
// arecord/aplay, sox, ffmpeg/ffplay/mpv). It is the fallback when no native API is usable.
type execBackend struct{}

func init() { register(execBackend{}, priorityTools) }

func (execBackend) Name() string { return "tools" }

func (execBackend) Available() bool { return true }

// FindTool locates an executable in PATH and common package manager locations.
func FindTool(names ...string) string {
	home := os.Getenv("HOME")
	searchPaths := []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin", "/usr/bin", "/bin"}
	if home != "" {
		searchPaths = append(searchPaths,
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, "bin"),
			filepath.Join(home, "go", "bin"),
			filepath.Join(home, "homebrew", "bin"),
			filepath.Join(home, ".homebrew", "bin"),
		)
	}
	if p, err := os.Executable(); err == nil {
		searchPaths = append([]string{filepath.Dir(p)}, searchPaths...)
	}
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
		for _, dir := range searchPaths {
			candidate := filepath.Join(dir, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
		}
	}
	return ""
}

func (execBackend) InputDevices() ([]Device, error) {
	devices := []Device{{ID: "default", Name: "Default System Microphone", IsDefault: true}}
	if p := FindTool("pactl"); p != "" {
		if out, err := exec.Command(p, "list", "sources").Output(); err == nil {
			if parsed := ParsePactlSources(out); len(parsed) > 0 {
				return append(devices, parsed...), nil
			}
		}
	}
	if p := FindTool("arecord"); p != "" {
		if out, err := exec.Command(p, "-l").Output(); err == nil {
			return append(devices, ParseAlsaDevices(out)...), nil
		}
	}
	return devices, nil
}

func (execBackend) OutputDevices() ([]Device, error) {
	devices := []Device{{ID: "default", Name: "Default System Output / Speakers", IsDefault: true}}
	if p := FindTool("pactl"); p != "" {
		if out, err := exec.Command(p, "list", "sinks").Output(); err == nil {
			if parsed := ParsePactlSinks(out); len(parsed) > 0 {
				return append(devices, parsed...), nil
			}
		}
	}
	if p := FindTool("aplay"); p != "" {
		if out, err := exec.Command(p, "-l").Output(); err == nil {
			return append(devices, ParseAlsaDevices(out)...), nil
		}
	}
	return devices, nil
}

type execStream struct {
	name string
	cmd  *exec.Cmd
	pipe io.Closer
	done chan struct{}
	once sync.Once
}

func (s *execStream) Backend() string { return s.name }

func (s *execStream) Close() error {
	s.once.Do(func() {
		close(s.done)
		if s.pipe != nil {
			_ = s.pipe.Close()
		}
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
			_, _ = s.cmd.Process.Wait()
		}
	})
	return nil
}

func rate() string { return strconv.Itoa(SampleRate) }

func captureCommands(deviceID string) []*exec.Cmd {
	custom := !isDefault(deviceID)
	var cmds []*exec.Cmd
	if runtime.GOOS == "darwin" {
		if p := FindTool("ffmpeg"); p != "" {
			input := ":default"
			if custom {
				input = ":" + deviceID
			}
			cmds = append(cmds, exec.Command(p, "-loglevel", "quiet", "-f", "avfoundation", "-i", input, "-ar", rate(), "-ac", "1", "-f", "s16le", "pipe:1"))
		}
		if p := FindTool("rec", "sox"); p != "" {
			args := []string{"-q", "-r", rate(), "-c", "1", "-b", "16", "-e", "signed-integer", "-t", "raw", "-"}
			if strings.HasSuffix(p, "sox") {
				args = append([]string{"-q", "-d"}, args[1:]...)
			}
			cmds = append(cmds, exec.Command(p, args...))
		}
		return cmds
	}
	if p := FindTool("parec"); p != "" {
		args := []string{"--rate=" + rate(), "--channels=1", "--format=s16le", "--latency-msec=20"}
		if custom {
			args = append([]string{"-d", deviceID}, args...)
		}
		cmds = append(cmds, exec.Command(p, args...))
	}
	if p := FindTool("pw-record"); p != "" {
		args := []string{"--rate", rate(), "--channels", "1", "--format", "s16", "-"}
		if custom {
			args = append([]string{"--target", deviceID}, args...)
		}
		cmds = append(cmds, exec.Command(p, args...))
	}
	if p := FindTool("arecord"); p != "" {
		args := []string{"-q", "-r", rate(), "-f", "S16_LE", "-c", "1", "-t", "raw"}
		if custom {
			args = append([]string{"-D", deviceID}, args...)
		}
		cmds = append(cmds, exec.Command(p, args...))
	}
	if p := FindTool("rec"); p != "" {
		cmd := exec.Command(p, "-q", "-r", rate(), "-c", "1", "-b", "16", "-e", "signed-integer", "-t", "raw", "-")
		if custom {
			cmd.Env = append(os.Environ(), "AUDIODEV="+deviceID)
		}
		cmds = append(cmds, cmd)
	}
	return cmds
}

func playbackCommands(deviceID string) []*exec.Cmd {
	custom := !isDefault(deviceID)
	var cmds []*exec.Cmd
	if p := FindTool("pacat"); p != "" && runtime.GOOS != "darwin" {
		args := []string{"--playback", "--rate=" + rate(), "--channels=1", "--format=s16le", "--latency-msec=40"}
		if custom {
			args = append([]string{"-d", deviceID}, args...)
		}
		cmds = append(cmds, exec.Command(p, args...))
	}
	if p := FindTool("pw-play"); p != "" {
		args := []string{"--rate", rate(), "--channels", "1", "--format", "s16", "-"}
		if custom {
			args = append([]string{"--target", deviceID}, args...)
		}
		cmds = append(cmds, exec.Command(p, args...))
	}
	if p := FindTool("aplay"); p != "" {
		args := []string{"-q", "-r", rate(), "-f", "S16_LE", "-c", "1", "-t", "raw"}
		if custom {
			args = append([]string{"-D", deviceID}, args...)
		}
		cmds = append(cmds, exec.Command(p, args...))
	}
	if p := FindTool("play"); p != "" {
		cmds = append(cmds, exec.Command(p, "-q", "-t", "raw", "-r", rate(), "-c", "1", "-b", "16", "-e", "signed-integer", "-"))
	}
	if p := FindTool("ffplay"); p != "" {
		cmds = append(cmds, exec.Command(p, "-loglevel", "quiet", "-nodisp", "-f", "s16le", "-ar", rate(), "-ac", "1", "-probesize", "32", "-analyzeduration", "0", "-fflags", "nobuffer", "-flags", "low_delay", "-i", "pipe:0"))
	}
	if p := FindTool("mpv"); p != "" {
		cmds = append(cmds, exec.Command(p, "--really-quiet", "--no-video", "--idle=yes", "--profile=low-latency", "--untimed", "--cache=no", "--demuxer=rawaudio", "--demuxer-rawaudio-rate="+rate(), "--demuxer-rawaudio-channels=1", "--demuxer-rawaudio-format=s16le", "-"))
	}
	return cmds
}

func (execBackend) OpenCapture(deviceID string, cb CaptureFunc) (Stream, error) {
	for _, cmd := range captureCommands(deviceID) {
		stdout, err := cmd.StdoutPipe()
		if err != nil || cmd.Start() != nil {
			continue
		}
		s := &execStream{name: filepath.Base(cmd.Path), cmd: cmd, pipe: stdout, done: make(chan struct{})}
		acc := newFrameAccumulator(cb)
		go func() {
			buf := make([]byte, FrameSamples*2)
			var carry []byte
			for {
				n, err := stdout.Read(buf)
				if n > 0 {
					acc.pushBytes(buf[:n], &carry)
				}
				if err != nil {
					return
				}
			}
		}()
		return s, nil
	}
	return nil, fmt.Errorf("audioio: no audio capture tool found (install PulseAudio/PipeWire tools, alsa-utils, sox or ffmpeg)")
}

func (execBackend) OpenPlayback(deviceID string, cb RenderFunc) (Stream, error) {
	for _, cmd := range playbackCommands(deviceID) {
		stdin, err := cmd.StdinPipe()
		if err != nil || cmd.Start() != nil {
			continue
		}
		s := &execStream{name: filepath.Base(cmd.Path), cmd: cmd, pipe: stdin, done: make(chan struct{})}
		go func() {
			adapter := newRenderAdapter(cb)
			buf := make([]byte, FrameSamples*2)
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-s.done:
					return
				case <-ticker.C:
				}
				adapter.fillBytes(buf)
				if _, err := stdin.Write(buf); err != nil {
					return
				}
			}
		}()
		return s, nil
	}
	return nil, fmt.Errorf("audioio: no audio playback tool found")
}

// ParsePactlSources parses `pactl list sources`, skipping monitor sources.
func ParsePactlSources(data []byte) []Device {
	return parsePactl(data, "Source #", true)
}

// ParsePactlSinks parses `pactl list sinks`.
func ParsePactlSinks(data []byte) []Device {
	return parsePactl(data, "Sink #", false)
}

func parsePactl(data []byte, header string, skipMonitors bool) []Device {
	var devices []Device
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var curName, curDesc string
	flush := func() {
		if curName != "" && !(skipMonitors && (strings.HasSuffix(curName, ".monitor") || strings.HasPrefix(curDesc, "Monitor of"))) {
			name := curDesc
			if name == "" {
				name = curName
			}
			devices = append(devices, Device{ID: curName, Name: name})
		}
		curName, curDesc = "", ""
	}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, header):
			flush()
		case strings.HasPrefix(line, "Name: "):
			curName = strings.TrimSpace(strings.TrimPrefix(line, "Name: "))
		case strings.HasPrefix(line, "Description: "):
			curDesc = strings.TrimSpace(strings.TrimPrefix(line, "Description: "))
		case strings.HasPrefix(line, "device.description = ") && curDesc == "":
			curDesc = strings.Trim(strings.TrimPrefix(line, "device.description = "), "\"")
		case strings.HasPrefix(line, "node.description = ") && curDesc == "":
			curDesc = strings.Trim(strings.TrimPrefix(line, "node.description = "), "\"")
		}
	}
	flush()
	return devices
}

// ParseAlsaDevices parses `arecord -l` / `aplay -l` output.
func ParseAlsaDevices(data []byte) []Device {
	var devices []Device
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "card ") {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) < 3 {
			continue
		}
		cardNum := strings.TrimSpace(strings.TrimPrefix(strings.Fields(parts[0])[1], "card"))
		devName := strings.TrimSpace(parts[1])
		if idx := strings.Index(devName, "["); idx != -1 {
			devName = strings.Trim(devName[idx:], "[]")
		}
		devices = append(devices, Device{ID: fmt.Sprintf("hw:%s,0", cardNum), Name: devName})
	}
	return devices
}
