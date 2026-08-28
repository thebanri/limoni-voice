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
	"sync/atomic"
)

// KittyFrame holds the latest decoded video frame in memory
type KittyFrame struct {
	RGB  []byte
	W    int
	H    int
	Left int
	Top  int
	Cols int
	Rows int
	Seq  uint64
}

var (
	latestFrameLock sync.RWMutex
	latestFrame     *KittyFrame
	frameCounter    uint64
	lastRenderedSeq uint64
)

// SetLatestKittyFrame stores the most recent decoded video frame
func SetLatestKittyFrame(f *KittyFrame) {
	latestFrameLock.Lock()
	latestFrame = f
	latestFrameLock.Unlock()
}

// ClearLatestKittyFrame clears stored frame and removes Kitty graphics from terminal
func ClearLatestKittyFrame() {
	latestFrameLock.Lock()
	latestFrame = nil
	lastRenderedSeq = 0
	latestFrameLock.Unlock()
	_, _ = os.Stdout.Write([]byte("\x1b_Ga=d,d=A\x1b\\"))
}

// StartNativeKittyReceiver runs FFmpeg to decode MPEG-TS into raw RGB24 frames and buffers them in memory
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

	// Background reader goroutine: decodes raw RGB frames and updates latestFrame buffer
	go func() {
		defer close(s.doneCh)
		defer ClearLatestKittyFrame()

		for {
			select {
			case <-sessionCtx.Done():
				return
			default:
			}

			frameBuf := make([]byte, frameBytes)
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

			seq := atomic.AddUint64(&frameCounter, 1)

			// Store latest frame for synchronized TUI rendering
			SetLatestKittyFrame(&KittyFrame{
				RGB:  frameBuf,
				W:    frameW,
				H:    frameH,
				Left: opt.Left,
				Top:  opt.Top,
				Cols: opt.Cols,
				Rows: opt.Rows,
				Seq:  seq,
			})
		}
	}()

	return s, nil
}

// RenderLatestKittyFrame is called strictly AFTER Limoni has flushed its frame buffer
func RenderLatestKittyFrame(left, top, cols, rows int) {
	latestFrameLock.RLock()
	frame := latestFrame
	latestFrameLock.RUnlock()

	if frame == nil || len(frame.RGB) == 0 {
		return
	}

	// Avoid re-transmitting identical frame if no new frame arrived
	if frame.Seq == lastRenderedSeq {
		return
	}
	lastRenderedSeq = frame.Seq

	if left <= 0 {
		left = frame.Left
	}
	if top <= 0 {
		top = frame.Top
	}
	if cols <= 0 {
		cols = frame.Cols
	}
	if rows <= 0 {
		rows = frame.Rows
	}

	encoded := base64.StdEncoding.EncodeToString(frame.RGB)
	chunkSize := 4096
	total := len(encoded)

	var sb bytes.Buffer

	// 1. Move cursor to (Top, Left) cell without disturbing TUI
	fmt.Fprintf(&sb, "\x1b7\x1b[%d;%dH", top, left)

	// 2. Transmit Kitty Graphics payload
	for i := 0; i < total; i += chunkSize {
		end := i + chunkSize
		m := 1
		if end >= total {
			end = total
			m = 0
		}
		chunk := encoded[i:end]
		if i == 0 {
			// a=T, f=24, s=W, v=H, c=cols, r=rows, i=1, q=2, C=1
			fmt.Fprintf(&sb, "\x1b_Ga=T,f=24,s=%d,v=%d,c=%d,r=%d,i=1,q=2,C=1,m=%d;%s\x1b\\",
				frame.W, frame.H, cols, rows, m, chunk)
		} else {
			fmt.Fprintf(&sb, "\x1b_Gm=%d;%s\x1b\\", m, chunk)
		}
	}

	// 3. Restore cursor
	sb.WriteString("\x1b8")

	_, _ = os.Stdout.Write(sb.Bytes())
}
