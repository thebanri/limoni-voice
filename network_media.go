package main

import (
	"fmt"
	"net"
	"time"

	"github.com/thebanri/limoni-voice/internal/protocol"
	"github.com/thebanri/limoni-voice/internal/voice"
)

func (n *P2PNode) encoder() *voice.Encoder {
	n.voiceEncOnce.Do(func() {
		enc, err := voice.NewEncoder(AudioSampleRate, AudioFrameSamples)
		if err != nil {
			n.log(fmt.Sprintf("[ERROR] Opus encoder unavailable: %v", err))
			return
		}
		n.voiceEnc = enc
	})
	return n.voiceEnc
}

// SendAudio encodes one captured PCM frame with Opus and sends it to the room.
func (n *P2PNode) SendAudio(rms float64, speaking bool, pcm []byte) {
	n.mu.RLock()
	active := n.IsConnected && len(n.Peers) > 0
	n.mu.RUnlock()
	if !active {
		return
	}
	enc := n.encoder()
	if enc == nil {
		return
	}
	// Every captured frame is encoded (including silence kept for the pre-roll) so the encoder
	// state stays continuous when speech starts.
	frame, err := enc.EncodePCM16LE(pcm)
	if err != nil {
		return
	}
	now := time.Now().UnixMilli()

	n.mu.Lock()
	if !n.IsConnected || len(n.Peers) == 0 {
		n.mu.Unlock()
		return
	}
	room := n.roomID

	// DTX (Discontinuous Transmission) with Pre-Roll Lookback Cushion:
	// When silent, maintain a 3-frame (~60ms) ring buffer. When speech begins, flush the
	// pre-buffered frames immediately so word onsets are never clipped.
	var flushedPreRoll []audioPreRollFrame
	if speaking {
		if n.silenceHangover == 0 && len(n.audioPreRoll) > 0 {
			flushedPreRoll = n.audioPreRoll
			n.audioPreRoll = nil
		}
		n.silenceHangover = 25
	} else {
		if n.silenceHangover > 0 {
			n.silenceHangover--
		} else {
			n.audioPreRoll = append(n.audioPreRoll, audioPreRollFrame{rms: rms, frame: frame, ts: now})
			if len(n.audioPreRoll) > 3 {
				n.audioPreRoll = n.audioPreRoll[len(n.audioPreRoll)-3:]
			}
			n.mu.Unlock()
			return
		}
	}

	muted, deafened := false, false
	if n.audio != nil {
		muted, deafened = n.audio.Muted, n.audio.Deafened
	}
	pktsToSend := make([]P2PPacket, 0, len(flushedPreRoll)+1)
	for _, pre := range flushedPreRoll {
		n.audioSeq++
		pktsToSend = append(pktsToSend, P2PPacket{
			Type: PacketAudio, RoomCode: room, SenderID: n.LocalID,
			IsMuted: muted, IsDeafened: deafened, Speaking: true,
			RMS: pre.rms, Seq: n.audioSeq, Timestamp: pre.ts, Payload: pre.frame,
		})
	}
	n.audioSeq++
	pktsToSend = append(pktsToSend, P2PPacket{
		Type: PacketAudio, RoomCode: room, SenderID: n.LocalID,
		IsMuted: muted, IsDeafened: deafened, Speaking: speaking,
		RMS: rms, Seq: n.audioSeq, Timestamp: now, Payload: frame,
	})
	n.mu.Unlock()

	for i := range pktsToSend {
		n.sendAudioToPeers(&pktsToSend[i])
	}
}

// sendAudioToPeers sends a realtime packet over the relay and directly to every known peer
// address (redundant paths; receivers deduplicate by sequence number).
func (n *P2PNode) sendAudioToPeers(pkt *P2PPacket) {
	n.sendRedundant(pkt, protocol.FrameRealtime)
}

func (n *P2PNode) sendRedundant(pkt *P2PPacket, relayClass byte) {
	n.mu.RLock()
	keyring := n.keyring
	isRelay := n.isRelayConnected
	if pkt.LocalPort == 0 {
		pkt.LocalPort = n.Port
	}
	if keyring == nil || (len(n.Peers) == 0 && !isRelay) {
		n.mu.RUnlock()
		return
	}
	type target struct {
		addr *net.UDPAddr
		conn *net.UDPConn
	}
	targets := make([]target, 0, len(n.Peers))
	for _, peer := range n.Peers {
		if peer.Addr != nil {
			targets = append(targets, target{peer.Addr, peer.conn})
		}
	}
	n.mu.RUnlock()

	data, err := sealPacket(pkt, keyring)
	if err != nil {
		return
	}
	if isRelay {
		n.sendRelayFrame(relayClass, data)
	}
	for _, t := range targets {
		n.writeUDP(data, t.addr, t.conn)
	}
}

// adaptEncoderToLoss raises Opus in-band FEC redundancy when receivers report packet loss.
func (n *P2PNode) adaptEncoderToLoss() {
	enc := n.voiceEnc
	if enc == nil {
		return
	}
	n.mu.RLock()
	worst := 0.0
	for _, p := range n.Peers {
		worst = max(worst, p.RemoteLossPct)
	}
	n.mu.RUnlock()
	hint := voice.DefaultLossHint
	if worst > 1 {
		hint = int(worst*1.5) + 2
	}
	enc.SetPacketLoss(hint)
}
