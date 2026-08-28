package screenshare

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// StartNativeKittyReceiver runs FFmpeg to decode MPEG-TS and streams via Zero-Copy Double-Buffered SHM to Kitty
func StartNativeKittyReceiver(ctx context.Context, port int, opt ReceiverOptions) (*Session, error) {
	ffmpegPath, err := FindExecutable("ffmpeg")
	if err != nil {
		return StartReceiving(ctx, port, opt)
	}

	streamURL := fmt.Sprintf("udp://0.0.0.0:%d?reuse=1&pkt_size=1316&buffer_size=131072", port)

	frameW := 854
	frameH := 480
	frameBytes := frameW * frameH * 3

	args := []string{
		"-loglevel", "quiet",
		"-flags", "low_delay",
		"-fflags", "nobuffer+fastseek+flush_packets",
		"-analyzeduration", "0",
		"-probesize", "32",
		"-i", streamURL,
		"-vf", fmt.Sprintf("scale=%d:%d", frameW, frameH),
		"-f", "rawvideo",
		"-pix_fmt", "rgb24",
		"-r", "60",
		"-",
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(sessionCtx, ffmpegPath, args...)
	setupProcessGroup(cmd)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create ffmpeg stdout pipe: %w", err)
	}
	cmd.Stderr = nil

	s := &Session{
		cmd:       cmd,
		ctx:       sessionCtx,
		cancel:    cancel,
		errCh:     make(chan error, 1),
		doneCh:    make(chan struct{}),
		isBroad:   false,
		targetURL: streamURL,
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to start ffmpeg native receiver: %w", err)
	}

	// Prepare zero-copy double-buffer file paths in RAM disk (/dev/shm on Linux or TempDir)
	shmDir := "/dev/shm"
	if runtime.GOOS == "windows" || !dirExists(shmDir) {
		shmDir = os.TempDir()
	}
	shmFile1 := filepath.Join(shmDir, "tty-graphics-protocol-video-1.raw")
	shmFile2 := filepath.Join(shmDir, "tty-graphics-protocol-video-2.raw")

	b64Path1 := base64.StdEncoding.EncodeToString([]byte(shmFile1))
	b64Path2 := base64.StdEncoding.EncodeToString([]byte(shmFile2))

	// Reader goroutine: streams via Double-Buffered SHM
	go func() {
		defer close(s.doneCh)
		defer func() {
			_, _ = os.Stdout.Write([]byte("\x1b_Ga=d,d=A\x1b\\"))
			_ = os.Remove(shmFile1)
			_ = os.Remove(shmFile2)
		}()

		frameBuf := make([]byte, frameBytes)
		currentBuffer := 1

		for {
			select {
			case <-sessionCtx.Done():
				return
			default:
			}

			_, err := io.ReadFull(stdoutPipe, frameBuf)
			if err != nil {
				if sessionCtx.Err() == nil && err != io.EOF {
					s.mu.Lock()
					if !s.stopped {
						select {
						case s.errCh <- err:
						default:
						}
					}
					s.mu.Unlock()
				}
				return
			}

			// Alternate between buffer 1 and 2 (Double Buffering)
			var targetFile, b64Path string
			var newID, oldID int
			if currentBuffer == 1 {
				targetFile = shmFile1
				b64Path = b64Path1
				newID = 1
				oldID = 2
				currentBuffer = 2
			} else {
				targetFile = shmFile2
				b64Path = b64Path2
				newID = 2
				oldID = 1
				currentBuffer = 1
			}

			// 1. Write raw bytes to RAM disk file (extremely fast, ~0.1ms)
			_ = os.WriteFile(targetFile, frameBuf, 0600)

			// 2. Transmit tiny command to Kitty (Zero-Copy, 0% CPU, instant GPU render)
			EmitSHMKittyFrame(b64Path, frameW, frameH, opt.Left, opt.Top, opt.Cols, opt.Rows, newID, oldID)
		}
	}()

	return s, nil
}

// EmitSHMKittyFrame sends an atomic double-buffered zero-copy Kitty escape sequence
func EmitSHMKittyFrame(b64Path string, w, h, left, top, cols, rows, newID, oldID int) {
	if left <= 0 {
		left = 32
	}
	if top <= 0 {
		top = 5
	}
	if cols <= 0 {
		cols = 70
	}
	if rows <= 0 {
		rows = 22
	}

	var sb bytes.Buffer

	// Move cursor to stage top-left
	fmt.Fprintf(&sb, "\x1b7\x1b[%d;%dH", top, left)

	// a=T (Transmit and display)
	// t=f (Read from temporary file / SHM)
	// f=24 (24-bit raw RGB)
	// s=w, v=h (Source dimensions)
	// c=cols, r=rows (Grid bounds)
	// i=newID (Image ID)
	// q=2 (Quiet mode)
	// C=1 (Do not move cursor)
	fmt.Fprintf(&sb, "\x1b_Ga=T,t=f,f=24,s=%d,v=%d,c=%d,r=%d,i=%d,q=2,C=1;%s\x1b\\",
		w, h, cols, rows, newID, b64Path)

	// Delete old buffer image to keep terminal memory pristine
	fmt.Fprintf(&sb, "\x1b_Ga=d,d=i,i=%d,q=2\x1b\\", oldID)

	// Restore cursor
	sb.WriteString("\x1b8")

	_, _ = os.Stdout.Write(sb.Bytes())
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
