package protocol

import (
	"bytes"
	"math/rand"
	"reflect"
	"testing"
)

func TestPacketRoundTrip(t *testing.T) {
	pkt := Packet{
		Type:            PacketWelcome,
		RoomCode:        "7492",
		SenderID:        "peer_1234_98765",
		Nickname:        "Tester",
		IsMuted:         true,
		IsDeafened:      true,
		Speaking:        true,
		RMS:             0.75,
		Seq:             0xfffffff0,
		Timestamp:       1757777777123,
		Payload:         []byte{1, 2, 3, 4},
		IsSharingScreen: true,
		VideoPort:       50100,
		VideoFPS:        120,
		LocalPort:       50001,
		PIN:             "1234",
		IsLocked:        true,
		FileMeta:        &FileMetadata{TransferID: "tf_1", FileName: "a.txt", FileSize: 1 << 33, TotalChunks: 3, ChunkIndex: 2, IsCode: true, Checksum: "abc"},
		Peers: []PeerSummary{
			{ID: "p2", Nickname: "Bob", AddrStr: "[2001:db8::1]:50000", LocalPort: 50000, IsMuted: true, VideoPort: 1, VideoFPS: 60},
			{ID: "p3", Nickname: "Eve", IsSharingScreen: true},
		},
		Epoch:    7,
		LossPct:  12,
		JitterMs: 31,
	}
	data, err := pkt.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var got Packet
	if err := got.UnmarshalBinary(data); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pkt, got) {
		t.Fatalf("round trip mismatch:\n want %+v\n got  %+v", pkt, got)
	}
}

func TestAudioPacketIsCompact(t *testing.T) {
	pkt := Packet{Type: PacketAudio, RoomCode: "7492", SenderID: "peer_1234_98765", Seq: 1, Timestamp: 1, RMS: 0.1, Speaking: true, Payload: make([]byte, 80)}
	data, _ := pkt.MarshalBinary()
	if len(data) > 130 {
		t.Fatalf("audio packet too large: %d bytes", len(data))
	}
}

func TestUnmarshalRejectsGarbage(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	buf := make([]byte, 256)
	for i := 0; i < 20000; i++ {
		n := rng.Intn(len(buf))
		rng.Read(buf[:n])
		if n > 0 && rng.Intn(2) == 0 {
			buf[0] = PacketVersion
		}
		var p Packet
		_ = p.UnmarshalBinary(buf[:n]) // must never panic
	}
	var p Packet
	if err := p.UnmarshalBinary([]byte{1, 2, 3}); err == nil {
		t.Fatal("short packet accepted")
	}
	valid, _ := (&Packet{Type: PacketPing, SenderID: "x"}).MarshalBinary()
	truncated := bytes.Clone(valid[:len(valid)-1])
	if err := p.UnmarshalBinary(truncated); err == nil {
		t.Fatal("truncated TLV accepted")
	}
}

func FuzzPacketUnmarshal(f *testing.F) {
	seed, _ := (&Packet{Type: PacketChatMessage, SenderID: "a", Payload: []byte("hi")}).MarshalBinary()
	f.Add(seed)
	f.Fuzz(func(t *testing.T, data []byte) {
		var p Packet
		if p.UnmarshalBinary(data) == nil {
			if _, err := p.MarshalBinary(); err != nil && len(data) <= MaxPacketSize {
				t.Fatalf("re-marshal failed: %v", err)
			}
		}
	})
}
