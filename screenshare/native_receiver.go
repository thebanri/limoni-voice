package screenshare

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// StartNativeKittyReceiver runs FFmpeg to decode MPEG-TS into raw RGB24 frames and streams directly to Kitty terminal
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

	// Reader goroutine: streams raw RGB frames directly to Kitty terminal without cursor collisions
	go func() {
		defer close(s.doneCh)
		defer func() {
			_, _ = os.Stdout.Write([]byte("\x1b_Ga=d,d=A\x1b\\"))
		}()

		frameBuf := make([]byte, frameBytes)

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

			// Transmit frame directly to Kitty GPU texture buffer
			DirectEmitKittyRGBFrame(frameBuf, frameW, frameH, opt.Left, opt.Top, opt.Cols, opt.Rows)
		}
	}()

	return s, nil
}

// DirectEmitKittyRGBFrame emits a single 24-bit RGB frame to stdout using Kitty Graphics Protocol
func DirectEmitKittyRGBFrame(rgb []byte, w, h, left, top, cols, rows int) {
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

	encoded := base64.StdEncoding.EncodeToString(rgb)
	chunkSize := 4096
	total := len(encoded)

	var sb bytes.Buffer

	// Save cursor and move to top-left cell of the stage
	fmt.Fprintf(&sb, "\x1b7\x1b[%d;%dH", top, left)

	for i := 0; i < total; i += chunkSize {
		end := i + chunkSize
		m := 1
		if end >= total {
			end = total
			m = 0
		}
		chunk := encoded[i:end]
		if i == 0 {
			// a=T, f=24, s=w, v=h, c=cols, r=rows, i=1, q=2, C=1
			fmt.Fprintf(&sb, "\x1b_Ga=T,f=24,s=%d,v=%d,c=%d,r=%d,i=1,q=2,C=1,m=%d;%s\x1b\\",
				w, h, cols, rows, m, chunk)
		} else {
			fmt.Fprintf(&sb, "\x1b_Gm=%d;%s\x1b\\", m, chunk)
		}
	}

	// Restore cursor
	sb.WriteString("\x1b8")

	_, _ = os.Stdout.Write(sb.Bytes())
}
