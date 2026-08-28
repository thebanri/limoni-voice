package screenshare

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

// NativeKittyReceiver implements high-performance in-terminal video streaming via FFmpeg + Go Kitty Engine
type NativeKittyReceiver struct {
	cmd       *exec.Cmd
	ctx       context.Context
	cancel    context.CancelFunc
	errCh     chan error
	doneCh    chan struct{}
	stopped   bool
	mu        sync.Mutex
	targetURL string
}

// StartNativeKittyReceiver runs FFmpeg to decode MPEG-TS into raw RGB24 frames and emits Kitty protocol commands
func StartNativeKittyReceiver(ctx context.Context, port int, opt ReceiverOptions) (*Session, error) {
	ffmpegPath, err := FindExecutable("ffmpeg")
	if err != nil {
		// Fallback to mpv if ffmpeg is somehow missing
		return StartReceiving(ctx, port, opt)
	}

	streamURL := fmt.Sprintf("udp://0.0.0.0:%d?reuse=1&pkt_size=1316&buffer_size=131072", port)

	frameW := 480
	frameH := 270
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
		"-r", "30",
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

	// Reader goroutine: pipe raw RGB frames to terminal using Kitty protocol
	go func() {
		defer close(s.doneCh)
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

			// Render directly to terminal via Kitty Graphics Protocol
			EmitKittyRGBFrame(frameBuf, frameW, frameH, opt.Left, opt.Top, opt.Cols, opt.Rows)
		}
	}()

	return s, nil
}

// EmitKittyRGBFrame emits a single 24-bit RGB frame to stdout using Kitty Graphics Protocol
func EmitKittyRGBFrame(rgb []byte, w, h, left, top, cols, rows int) {
	encoded := base64.StdEncoding.EncodeToString(rgb)
	chunkSize := 4096
	total := len(encoded)

	var sb bytes.Buffer
	for i := 0; i < total; i += chunkSize {
		end := i + chunkSize
		m := 1
		if end >= total {
			end = total
			m = 0
		}
		chunk := encoded[i:end]
		if i == 0 {
			// a=T (Transmit and display immediately)
			// f=24 (24-bit RGB)
			// s=width, v=height (pixels)
			// c=cols, r=rows (cell grid bounds - prevents overflowing)
			// X=left, Y=top (1-based cell coordinate placement)
			// C=1 (Do not move cursor)
			fmt.Fprintf(&sb, "\x1b_Ga=T,f=24,s=%d,v=%d,c=%d,r=%d,X=%d,Y=%d,C=1,m=%d;%s\x1b\\",
				w, h, cols, rows, left, top, m, chunk)
		} else {
			fmt.Fprintf(&sb, "\x1b_Gm=%d;%s\x1b\\", m, chunk)
		}
	}
	_, _ = os.Stdout.Write(sb.Bytes())
}
