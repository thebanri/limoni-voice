package screenshare

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

// ANSIFrame holds the latest decoded RGB24 pixels scaled exactly to terminal cells
type ANSIFrame struct {
	RGB  []byte
	W    int // Pixel width = Cols
	H    int // Pixel height = Rows * 2
	Cols int
	Rows int
	Seq  uint64
}

var (
	latestANSIFrameLock sync.RWMutex
	latestANSIFrame     *ANSIFrame
	ansiFrameCounter    uint64
)

// SetLatestANSIFrame stores the most recent decoded video frame for ANSI rendering
func SetLatestANSIFrame(f *ANSIFrame) {
	latestANSIFrameLock.Lock()
	latestANSIFrame = f
	latestANSIFrameLock.Unlock()
}

// GetLatestANSIFrame retrieves the latest video frame for direct Limoni buffer drawing
func GetLatestANSIFrame() *ANSIFrame {
	latestANSIFrameLock.RLock()
	defer latestANSIFrameLock.RUnlock()
	return latestANSIFrame
}

// ClearLatestANSIFrame clears the buffered frame
func ClearLatestANSIFrame() {
	latestANSIFrameLock.Lock()
	latestANSIFrame = nil
	latestANSIFrameLock.Unlock()
}

// StartANSIReceiver runs FFmpeg to decode MPEG-TS into exact cell-sized RGB24 pixels (2 pixels per cell row via '▀')
func StartANSIReceiver(ctx context.Context, port int, cols, rows int) (*Session, error) {
	ffmpegPath, err := FindExecutable("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("ffmpeg not found: %w", err)
	}

	if cols <= 0 {
		cols = 70
	}
	if rows <= 0 {
		rows = 22
	}

	// 1 cell = 1 col wide, 2 pixels high (Upper half block '▀')
	pixelW := cols
	pixelH := rows * 2
	frameBytes := pixelW * pixelH * 3

	streamURL := fmt.Sprintf("udp://0.0.0.0:%d?reuse=1&pkt_size=1316&buffer_size=131072", port)

	args := []string{
		"-loglevel", "quiet",
		"-flags", "low_delay",
		"-fflags", "nobuffer+fastseek+flush_packets",
		"-analyzeduration", "0",
		"-probesize", "32",
		"-i", streamURL,
		"-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2", pixelW, pixelH, pixelW, pixelH),
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
		return nil, fmt.Errorf("failed to start ffmpeg ANSI receiver: %w", err)
	}

	// Background reader goroutine
	go func() {
		defer close(s.doneCh)
		defer ClearLatestANSIFrame()

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

			ansiFrameCounter++
			SetLatestANSIFrame(&ANSIFrame{
				RGB:  frameBuf,
				W:    pixelW,
				H:    pixelH,
				Cols: cols,
				Rows: rows,
				Seq:  ansiFrameCounter,
			})
		}
	}()

	return s, nil
}
