package relay

import (
	"encoding/json"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/thebanri/limoni-voice/internal/protocol"
)

// client is one WebSocket connection. Identity and room fields are guarded by Server.mu;
// the queues and UDP binding are safe for concurrent use.
type client struct {
	s    *Server
	conn *websocket.Conn
	ip   string

	id           string
	nickname     string
	roomCode     string
	pendingRoom  string
	pendingSince time.Time
	endpoint     protocol.Endpoint
	udpToken     [protocol.UDPTokenSize]byte
	hasUDPToken  bool

	ctrl     chan []byte
	realtime chan []byte
	reliable chan []byte
	bulk     chan []byte

	udpAddr atomic.Pointer[net.UDPAddr]
	udpSeen atomic.Int64

	done      chan struct{}
	closeOnce sync.Once
}

func newClient(s *Server, conn *websocket.Conn, ip string) *client {
	return &client{
		s:        s,
		conn:     conn,
		ip:       ip,
		ctrl:     make(chan []byte, 256),
		realtime: make(chan []byte, 256),
		reliable: make(chan []byte, 512),
		bulk:     make(chan []byte, 128),
		done:     make(chan struct{}),
	}
}

func (c *client) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

func (c *client) sendSignal(sig protocol.Signal) {
	data, err := json.Marshal(sig)
	if err != nil {
		return
	}
	select {
	case <-c.done:
	case c.ctrl <- data:
	default:
		// A client that cannot keep up with signalling is dropped rather than stalling the room.
		c.close()
	}
}

// enqueueFrame queues an encrypted frame for WebSocket delivery with class-based scheduling.
func (c *client) enqueueFrame(class byte, payload []byte) {
	frame := make([]byte, 1+len(payload))
	frame[0] = class
	copy(frame[1:], payload)
	switch class {
	case protocol.FrameBulk:
		select {
		case c.bulk <- frame:
		default:
			// Receiver fell behind on video: drop half of the backlog to snap back to real time.
			for i := len(c.bulk) / 2; i > 0; i-- {
				select {
				case <-c.bulk:
				default:
				}
			}
			select {
			case c.bulk <- frame:
			default:
			}
			c.s.metrics.framesDropped.Add(1)
		}
	case protocol.FrameReliable:
		select {
		case c.reliable <- frame:
		default:
			c.s.metrics.framesDropped.Add(1)
		}
	default:
		select {
		case c.realtime <- frame:
		default:
			c.s.metrics.framesDropped.Add(1)
		}
	}
}

func (c *client) udpPeer() *net.UDPAddr {
	if time.Since(time.Unix(0, c.udpSeen.Load())) > udpBindingTTL {
		return nil
	}
	return c.udpAddr.Load()
}

func (c *client) writePump() {
	ticker := time.NewTicker(20 * time.Second)
	defer func() {
		ticker.Stop()
		c.close()
	}()

	write := func(msgType int, data []byte) bool {
		_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := c.conn.WriteMessage(msgType, data); err != nil {
			return false
		}
		c.s.metrics.wsBytesOut.Add(uint64(len(data)))
		return true
	}

	for {
		// Strict priority: signalling, then realtime audio / ping, then reliable, then video.
		select {
		case data := <-c.ctrl:
			if !write(websocket.TextMessage, data) {
				return
			}
			continue
		default:
		}
		select {
		case data := <-c.realtime:
			if !write(websocket.BinaryMessage, data) {
				return
			}
			continue
		default:
		}

		select {
		case <-c.done:
			return
		case data := <-c.ctrl:
			if !write(websocket.TextMessage, data) {
				return
			}
		case data := <-c.realtime:
			if !write(websocket.BinaryMessage, data) {
				return
			}
		case data := <-c.reliable:
			if !write(websocket.BinaryMessage, data) {
				return
			}
		case data := <-c.bulk:
			if !write(websocket.BinaryMessage, data) {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
