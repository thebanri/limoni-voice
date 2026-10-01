package p2p

import (
	"encoding/binary"
	"strconv"
	"time"

	"github.com/thebanri/limoni-voice/internal/protocol"
)

const (
	// chatHistoryLimit is how many messages a member keeps to hand to members who join later.
	chatHistoryLimit = 200
	// chatHistoryBatch bounds the payload of one history packet (a longer message goes alone).
	chatHistoryBatch = 8 * 1024
	// chatPartSize is the most text one packet carries; a longer message goes in parts.
	chatPartSize = 16 * 1024
	// chatPartTimeout is how long the parts of a long message may take to all arrive.
	chatPartTimeout = time.Minute
)

// chatHistoryDelays are when a member sends its history to a member that joined: after the
// joiner settled in (its group key, its path), and once more in case a datagram was lost.
// The joiner drops what it already has, so the repeat costs nothing on screen.
var chatHistoryDelays = []time.Duration{2 * time.Second, 8 * time.Second}

// ChatEntry is one chat message of the room's history.
type ChatEntry struct {
	SenderID  string
	Nickname  string
	Text      string
	Timestamp time.Time // millisecond precision, as on the wire
}

func (e ChatEntry) key() string {
	return e.SenderID + "\x00" + strconv.FormatInt(e.Timestamp.UnixMilli(), 10) + "\x00" + e.Text
}

// recordChat keeps a message for members who join later. It reports false for one already kept.
func (n *P2PNode) recordChat(e ChatEntry) bool {
	n.chatMu.Lock()
	defer n.chatMu.Unlock()
	if n.chatSeen == nil {
		n.chatSeen = make(map[string]struct{})
	}
	k := e.key()
	if _, ok := n.chatSeen[k]; ok {
		return false
	}
	n.chatSeen[k] = struct{}{}
	n.chatLog = append(n.chatLog, e)
	if len(n.chatLog) > chatHistoryLimit {
		for _, old := range n.chatLog[:len(n.chatLog)-chatHistoryLimit] {
			delete(n.chatSeen, old.key())
		}
		n.chatLog = append([]ChatEntry(nil), n.chatLog[len(n.chatLog)-chatHistoryLimit:]...)
	}
	return true
}

// resetChatHistory forgets the room's messages when leaving it.
func (n *P2PNode) resetChatHistory() {
	n.chatMu.Lock()
	n.chatLog, n.chatSeen = nil, nil
	n.chatMu.Unlock()
}

// shareChatHistoryLater hands the chat so far to a member that just joined, so it sees what
// was said before it came in.
func (n *P2PNode) shareChatHistoryLater(peerID string) {
	for _, d := range chatHistoryDelays {
		time.AfterFunc(d, func() { n.sendChatHistory(peerID) })
	}
}

func (n *P2PNode) sendChatHistory(peerID string) {
	n.mu.RLock()
	_, isPeer := n.Peers[peerID]
	connected, room := n.IsConnected, n.roomID
	n.mu.RUnlock()
	if !connected || !isPeer {
		return
	}
	n.chatMu.Lock()
	var entries []ChatEntry
	for _, e := range n.chatLog {
		if len(e.Text) > chatPartSize {
			n.chatMu.Unlock()
			n.sendChatParts(e, room, 0, peerID) // too long for a history packet
			n.chatMu.Lock()
			continue
		}
		entries = append(entries, e)
	}
	n.chatMu.Unlock()

	for _, payload := range encodeChatHistory(entries) {
		pkt := P2PPacket{
			Type:      PacketChatHistory,
			RoomCode:  room,
			SenderID:  n.LocalID,
			Nickname:  n.Nickname,
			TargetID:  peerID,
			Payload:   payload,
			Timestamp: time.Now().UnixMilli(),
		}
		n.sendToMember(peerID, &pkt, protocol.FrameReliable)
	}
}

// handleChatHistoryLocked takes the messages a member sent us on joining, leaving out the
// ones we already have (every member sends its history, and sends it twice).
func (n *P2PNode) handleChatHistoryLocked(pkt *P2PPacket) {
	if pkt.TargetID != n.LocalID {
		return
	}
	var fresh []ChatEntry
	for _, e := range decodeChatHistory(pkt.Payload) {
		if n.recordChat(e) {
			fresh = append(fresh, e)
		}
	}
	if len(fresh) > 0 && n.OnChatHistory != nil {
		go n.OnChatHistory(fresh)
	}
}

// encodeChatHistory packs entries into payloads of about chatHistoryBatch bytes. Each entry is
// [id len u8][id][nick len u8][nick][timestamp ms i64][text len u16][text].
func encodeChatHistory(entries []ChatEntry) [][]byte {
	var out [][]byte
	var cur []byte
	for _, e := range entries {
		id, nick, text := clip(e.SenderID, 255), clip(e.Nickname, 255), clip(e.Text, chatPartSize)
		size := 1 + len(id) + 1 + len(nick) + 8 + 2 + len(text)
		if len(cur) > 0 && len(cur)+size > chatHistoryBatch {
			out = append(out, cur)
			cur = nil
		}
		cur = append(cur, byte(len(id)))
		cur = append(cur, id...)
		cur = append(cur, byte(len(nick)))
		cur = append(cur, nick...)
		cur = binary.BigEndian.AppendUint64(cur, uint64(e.Timestamp.UnixMilli()))
		cur = binary.BigEndian.AppendUint16(cur, uint16(len(text)))
		cur = append(cur, text...)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// decodeChatHistory reads the entries of one payload, stopping at the first malformed one.
func decodeChatHistory(b []byte) []ChatEntry {
	var out []ChatEntry
	str := func(n int) (string, bool) {
		if n > len(b) {
			return "", false
		}
		s := string(b[:n])
		b = b[n:]
		return s, true
	}
	for len(b) > 0 {
		var e ChatEntry
		var ok bool
		n := int(b[0])
		b = b[1:]
		if e.SenderID, ok = str(n); !ok || e.SenderID == "" || len(b) < 1 {
			return out
		}
		n = int(b[0])
		b = b[1:]
		if e.Nickname, ok = str(n); !ok || len(b) < 10 {
			return out
		}
		e.Timestamp = time.UnixMilli(int64(binary.BigEndian.Uint64(b)))
		n = int(binary.BigEndian.Uint16(b[8:]))
		b = b[10:]
		if e.Text, ok = str(n); !ok || e.Text == "" {
			return out
		}
		out = append(out, e)
	}
	return out
}

// chatAssembly collects the parts of one long message.
type chatAssembly struct {
	entry   ChatEntry
	history bool
	seq     uint32
	parts   [][]byte
	missing int
	started time.Time
}

// sendChatParts sends a message too long for one packet in parts of chatPartSize. A part for
// a member that joined (to non-empty) is part of its history.
func (n *P2PNode) sendChatParts(e ChatEntry, room string, seq uint32, to string) {
	parts := splitChat(e.Text)
	for i, chunk := range parts {
		pkt := P2PPacket{
			Type:      PacketChatPart,
			RoomCode:  room,
			SenderID:  n.LocalID,
			Nickname:  n.Nickname,
			Seq:       seq,
			TargetID:  to,
			Payload:   encodeChatPart(e, to != "", i, len(parts), chunk),
			Timestamp: e.Timestamp.UnixMilli(),
		}
		if to != "" {
			n.sendToMember(to, &pkt, protocol.FrameReliable)
		} else {
			n.sendToRoom(&pkt, protocol.FrameReliable)
		}
	}
}

// splitChat cuts text into pieces of at most chatPartSize bytes.
func splitChat(text string) []string {
	var parts []string
	for len(text) > chatPartSize {
		parts = append(parts, text[:chatPartSize])
		text = text[chatPartSize:]
	}
	return append(parts, text)
}

// handleChatPartLocked puts a long message together from its parts and shows it once they are
// all in. Parts come twice (directly and through the relay) and in any order.
func (n *P2PNode) handleChatPartLocked(pkt *P2PPacket) {
	e, history, idx, total, chunk, ok := decodeChatPart(pkt.Payload)
	if !ok || total > MaxChatBytes/chatPartSize+1 {
		return
	}
	if history && pkt.TargetID != n.LocalID {
		return
	}
	if !history && e.SenderID != pkt.SenderID {
		return // a live message is the sender's own
	}
	key := e.key() + "\x00" + strconv.Itoa(total)
	if history {
		key += "\x00h"
	}

	n.chatMu.Lock()
	now := time.Now()
	for k, a := range n.chatParts {
		if now.Sub(a.started) > chatPartTimeout {
			delete(n.chatParts, k)
		}
	}
	for k, at := range n.chatPartsDone {
		if now.Sub(at) > chatPartTimeout {
			delete(n.chatPartsDone, k)
		}
	}
	if _, done := n.chatPartsDone[key]; done {
		n.chatMu.Unlock()
		return
	}
	if n.chatParts == nil {
		n.chatParts = make(map[string]*chatAssembly)
	}
	a := n.chatParts[key]
	if a == nil {
		if len(n.chatParts) >= 16 {
			n.chatMu.Unlock()
			return // more long messages in flight than any room sends
		}
		a = &chatAssembly{entry: e, history: history, seq: pkt.Seq, parts: make([][]byte, total), missing: total, started: now}
		n.chatParts[key] = a
	}
	if a.parts[idx] == nil {
		a.parts[idx] = chunk
		a.missing--
	}
	if a.missing > 0 {
		n.chatMu.Unlock()
		return
	}
	delete(n.chatParts, key)
	if n.chatPartsDone == nil {
		n.chatPartsDone = make(map[string]time.Time)
	}
	n.chatPartsDone[key] = now
	n.chatMu.Unlock()

	var text []byte
	for _, p := range a.parts {
		text = append(text, p...)
	}
	a.entry.Text = ClipChat(string(text))
	if a.history {
		if n.recordChat(a.entry) && n.OnChatHistory != nil {
			go n.OnChatHistory([]ChatEntry{a.entry})
		}
		return
	}
	if !n.chatDedup.ShouldProcess(a.entry.SenderID, a.seq, a.entry.Timestamp.UnixMilli(), a.entry.Text) || !n.recordChat(a.entry) {
		return
	}
	if n.OnChatMessage != nil {
		go n.OnChatMessage(a.entry.SenderID, a.entry.Nickname, a.entry.Text, a.entry.Timestamp)
	}
}

// encodeChatPart lays out one part: [history u8][id len u8][id][nick len u8][nick]
// [timestamp ms i64][index u16][total u16][text].
func encodeChatPart(e ChatEntry, history bool, idx, total int, chunk string) []byte {
	id, nick := clip(e.SenderID, 255), clip(e.Nickname, 255)
	b := make([]byte, 0, 15+len(id)+len(nick)+len(chunk))
	var h byte
	if history {
		h = 1
	}
	b = append(b, h, byte(len(id)))
	b = append(b, id...)
	b = append(b, byte(len(nick)))
	b = append(b, nick...)
	b = binary.BigEndian.AppendUint64(b, uint64(e.Timestamp.UnixMilli()))
	b = binary.BigEndian.AppendUint16(b, uint16(idx))
	b = binary.BigEndian.AppendUint16(b, uint16(total))
	return append(b, chunk...)
}

func decodeChatPart(b []byte) (e ChatEntry, history bool, idx, total int, chunk []byte, ok bool) {
	if len(b) < 2 {
		return
	}
	history = b[0] == 1
	n := int(b[1])
	b = b[2:]
	if len(b) < n+1 || n == 0 {
		return
	}
	e.SenderID = string(b[:n])
	b = b[n:]
	n = int(b[0])
	b = b[1:]
	if len(b) < n+12 {
		return
	}
	e.Nickname = string(b[:n])
	b = b[n:]
	e.Timestamp = time.UnixMilli(int64(binary.BigEndian.Uint64(b)))
	idx, total = int(binary.BigEndian.Uint16(b[8:])), int(binary.BigEndian.Uint16(b[10:]))
	chunk = append([]byte(nil), b[12:]...)
	ok = total > 0 && idx < total && len(chunk) > 0 && len(chunk) <= chatPartSize
	return
}

func clip(s string, max int) string {
	if len(s) > max {
		return s[:max]
	}
	return s
}
