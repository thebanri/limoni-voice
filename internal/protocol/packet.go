// Package protocol defines the Limoni Voice wire formats shared by the client and the relay:
// the compact binary P2P packet codec (carried inside the AEAD envelope) and the JSON
// signalling messages exchanged with the relay server.
package protocol

import (
	"encoding/binary"
	"errors"
	"math"
)

// PacketVersion is the binary packet format version (v1 was encoding/gob).
const PacketVersion = 2

// MaxPacketSize bounds a decoded packet to a single UDP datagram / WebSocket frame.
const MaxPacketSize = 65535

type PacketType byte

const (
	PacketHello PacketType = iota + 1
	PacketWelcome
	PacketPing
	PacketPong
	PacketAudio
	PacketMuteState
	PacketLeave
	PacketJoinRequest // Client probes network to request joining an open host's room
	PacketRoomFull    // Host rejects join request because room reached 4-peer capacity
	PacketScreenShareStart
	PacketScreenShareStop
	PacketScreenShareData
	PacketChatMessage
	PacketPortHop
	PacketRoomLocked // Host rejects join request because room is locked or PIN is invalid
	PacketFileHeader // Initiates an E2EE direct file or code snippet transfer
	PacketFileChunk  // Chunks of encrypted file data
	PacketFileAck    // Transfer delivery completion or cancellation
	PacketRekey      // Host distributes a fresh room group key (Payload = sealed key, Epoch = new epoch)
	PacketRekeyAck   // Member confirms installation of the group key for Epoch

	// Screen share v2 (watcher-driven delivery). Older peers ignore unknown packet types.
	PacketScreenWatch   // Viewer → sharer (TargetID): start / keep watching; LossPct = video loss since last report
	PacketScreenUnwatch // Viewer → sharer (TargetID): stop watching
	PacketScreenNack    // Viewer → sharer (TargetID): Payload = missing video sequence numbers (see AppendSeqList)
	PacketScreenAudio   // Sharer → watchers: Opus system audio frame (Seq, Timestamp, Payload)
)

// FileMetadata describes a chunked file / code snippet transfer.
type FileMetadata struct {
	TransferID  string `json:"transfer_id"`
	FileName    string `json:"file_name"`
	FileSize    int64  `json:"file_size"`
	TotalChunks int    `json:"total_chunks"`
	ChunkIndex  int    `json:"chunk_index"`
	IsCode      bool   `json:"is_code"`
	Checksum    string `json:"checksum"`
}

// PeerSummary is the per-member entry of a Welcome packet (mesh introduction).
type PeerSummary struct {
	ID              string
	Nickname        string
	AddrStr         string
	LocalPort       int
	IsMuted         bool
	IsDeafened      bool
	IsSharingScreen bool
	VideoPort       int
	VideoFPS        int
}

// Packet is the decrypted P2P packet exchanged between room members.
type Packet struct {
	Type            PacketType    `json:"type"`
	RoomCode        string        `json:"room_code"`
	SenderID        string        `json:"sender_id"`
	Nickname        string        `json:"nickname"`
	IsMuted         bool          `json:"is_muted"`
	IsDeafened      bool          `json:"is_deafened"`
	Speaking        bool          `json:"speaking"`
	RMS             float64       `json:"rms"`
	Seq             uint32        `json:"seq"`
	Timestamp       int64         `json:"timestamp"`
	Payload         []byte        `json:"payload"`
	IsSharingScreen bool          `json:"is_sharing_screen"`
	VideoPort       int           `json:"video_port"`
	VideoFPS        int           `json:"video_fps,omitempty"`
	LocalPort       int           `json:"local_port"`
	PIN             string        `json:"pin,omitempty"`
	IsLocked        bool          `json:"is_locked,omitempty"`
	FileMeta        *FileMetadata `json:"file_meta,omitempty"`
	Peers           []PeerSummary `json:"peers,omitempty"`
	Epoch           uint32        `json:"epoch,omitempty"`
	LossPct         uint8         `json:"loss_pct,omitempty"` // receiver report: audio loss % observed from the ping target
	JitterMs        uint16        `json:"jitter_ms,omitempty"`
	TargetID        string        `json:"target_id,omitempty"` // addressed member for point-to-point control packets
	VideoKbps       uint32        `json:"video_kbps,omitempty"`
	HasAudio        bool          `json:"has_audio,omitempty"` // screen share carries system audio
}

// Fixed header layout (20 bytes):
//
//	[0] version  [1] type  [2:4] flags  [4:8] seq  [8:16] timestamp ms  [16:20] rms float32
const headerSize = 20

const (
	flagMuted = 1 << iota
	flagDeafened
	flagSpeaking
	flagSharing
	flagLocked
	flagHasAudio
)

// TLV tags for optional fields. Unknown tags are skipped so newer peers stay compatible.
const (
	tagRoomCode byte = iota + 1
	tagSenderID
	tagNickname
	tagPayload
	tagVideoPort
	tagVideoFPS
	tagLocalPort
	tagPIN
	tagFileMeta
	tagPeers
	tagEpoch
	tagLossPct
	tagJitterMs
	tagTargetID
	tagVideoKbps
)

const (
	fmTransferID byte = iota + 1
	fmFileName
	fmFileSize
	fmTotalChunks
	fmChunkIndex
	fmIsCode
	fmChecksum
)

const (
	psID byte = iota + 1
	psNickname
	psAddr
	psLocalPort
	psFlags
	psVideoPort
	psVideoFPS
)

var (
	ErrShortPacket   = errors.New("protocol: packet too short")
	ErrVersion       = errors.New("protocol: unsupported packet version")
	ErrMalformed     = errors.New("protocol: malformed packet")
	ErrPacketTooLong = errors.New("protocol: packet exceeds maximum size")
)

// AppendBinary appends the binary encoding of p to dst.
func (p *Packet) AppendBinary(dst []byte) []byte {
	var hdr [headerSize]byte
	hdr[0] = PacketVersion
	hdr[1] = byte(p.Type)
	var flags uint16
	if p.IsMuted {
		flags |= flagMuted
	}
	if p.IsDeafened {
		flags |= flagDeafened
	}
	if p.Speaking {
		flags |= flagSpeaking
	}
	if p.IsSharingScreen {
		flags |= flagSharing
	}
	if p.IsLocked {
		flags |= flagLocked
	}
	if p.HasAudio {
		flags |= flagHasAudio
	}
	binary.BigEndian.PutUint16(hdr[2:4], flags)
	binary.BigEndian.PutUint32(hdr[4:8], p.Seq)
	binary.BigEndian.PutUint64(hdr[8:16], uint64(p.Timestamp))
	binary.BigEndian.PutUint32(hdr[16:20], math.Float32bits(float32(p.RMS)))
	dst = append(dst, hdr[:]...)

	dst = appendString(dst, tagRoomCode, p.RoomCode)
	dst = appendString(dst, tagSenderID, p.SenderID)
	dst = appendString(dst, tagNickname, p.Nickname)
	if len(p.Payload) > 0 {
		dst = appendBytes(dst, tagPayload, p.Payload)
	}
	dst = appendUint(dst, tagVideoPort, uint64(max(p.VideoPort, 0)))
	dst = appendUint(dst, tagVideoFPS, uint64(max(p.VideoFPS, 0)))
	dst = appendUint(dst, tagLocalPort, uint64(max(p.LocalPort, 0)))
	dst = appendString(dst, tagPIN, p.PIN)
	if p.FileMeta != nil {
		dst = appendBytes(dst, tagFileMeta, encodeFileMeta(nil, p.FileMeta))
	}
	if len(p.Peers) > 0 {
		dst = appendBytes(dst, tagPeers, encodePeers(nil, p.Peers))
	}
	dst = appendUint(dst, tagEpoch, uint64(p.Epoch))
	dst = appendUint(dst, tagLossPct, uint64(p.LossPct))
	dst = appendUint(dst, tagJitterMs, uint64(p.JitterMs))
	dst = appendString(dst, tagTargetID, p.TargetID)
	dst = appendUint(dst, tagVideoKbps, uint64(p.VideoKbps))
	return dst
}

// MarshalBinary encodes the packet.
func (p *Packet) MarshalBinary() ([]byte, error) {
	out := p.AppendBinary(make([]byte, 0, headerSize+64+len(p.Payload)))
	if len(out) > MaxPacketSize {
		return nil, ErrPacketTooLong
	}
	return out, nil
}

// UnmarshalBinary decodes data into p, replacing all fields. Byte slices in p alias data.
func (p *Packet) UnmarshalBinary(data []byte) error {
	*p = Packet{}
	if len(data) < headerSize {
		return ErrShortPacket
	}
	if data[0] != PacketVersion {
		return ErrVersion
	}
	if len(data) > MaxPacketSize {
		return ErrPacketTooLong
	}
	p.Type = PacketType(data[1])
	flags := binary.BigEndian.Uint16(data[2:4])
	p.IsMuted = flags&flagMuted != 0
	p.IsDeafened = flags&flagDeafened != 0
	p.Speaking = flags&flagSpeaking != 0
	p.IsSharingScreen = flags&flagSharing != 0
	p.IsLocked = flags&flagLocked != 0
	p.HasAudio = flags&flagHasAudio != 0
	p.Seq = binary.BigEndian.Uint32(data[4:8])
	p.Timestamp = int64(binary.BigEndian.Uint64(data[8:16]))
	rms := math.Float32frombits(binary.BigEndian.Uint32(data[16:20]))
	if math.IsNaN(float64(rms)) || math.IsInf(float64(rms), 0) {
		rms = 0
	}
	p.RMS = float64(rms)

	return walkTLV(data[headerSize:], func(tag byte, val []byte) error {
		switch tag {
		case tagRoomCode:
			p.RoomCode = string(val)
		case tagSenderID:
			p.SenderID = string(val)
		case tagNickname:
			p.Nickname = string(val)
		case tagPayload:
			p.Payload = val
		case tagVideoPort:
			p.VideoPort = int(clampUvarint(val, math.MaxUint16))
		case tagVideoFPS:
			p.VideoFPS = int(clampUvarint(val, 1000))
		case tagLocalPort:
			p.LocalPort = int(clampUvarint(val, math.MaxUint16))
		case tagPIN:
			p.PIN = string(val)
		case tagFileMeta:
			fm, err := decodeFileMeta(val)
			if err != nil {
				return err
			}
			p.FileMeta = fm
		case tagPeers:
			peers, err := decodePeers(val)
			if err != nil {
				return err
			}
			p.Peers = peers
		case tagEpoch:
			p.Epoch = uint32(clampUvarint(val, math.MaxUint32))
		case tagLossPct:
			p.LossPct = uint8(clampUvarint(val, 100))
		case tagJitterMs:
			p.JitterMs = uint16(clampUvarint(val, math.MaxUint16))
		case tagTargetID:
			p.TargetID = string(val)
		case tagVideoKbps:
			p.VideoKbps = uint32(clampUvarint(val, math.MaxUint32))
		}
		return nil
	})
}

func appendTLVHeader(dst []byte, tag byte, n int) []byte {
	dst = append(dst, tag)
	return binary.AppendUvarint(dst, uint64(n))
}

func appendBytes(dst []byte, tag byte, v []byte) []byte {
	dst = appendTLVHeader(dst, tag, len(v))
	return append(dst, v...)
}

func appendString(dst []byte, tag byte, v string) []byte {
	if v == "" {
		return dst
	}
	dst = appendTLVHeader(dst, tag, len(v))
	return append(dst, v...)
}

func appendUint(dst []byte, tag byte, v uint64) []byte {
	if v == 0 {
		return dst
	}
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	return appendBytes(dst, tag, tmp[:n])
}

func walkTLV(data []byte, fn func(tag byte, val []byte) error) error {
	for len(data) > 0 {
		tag := data[0]
		length, n := binary.Uvarint(data[1:])
		if n <= 0 || length > uint64(len(data)-1-n) {
			return ErrMalformed
		}
		start := 1 + n
		end := start + int(length)
		if err := fn(tag, data[start:end:end]); err != nil {
			return err
		}
		data = data[end:]
	}
	return nil
}

func clampUvarint(val []byte, limit uint64) uint64 {
	v, n := binary.Uvarint(val)
	if n <= 0 {
		return 0
	}
	if v > limit {
		return limit
	}
	return v
}

func encodeFileMeta(dst []byte, fm *FileMetadata) []byte {
	dst = appendString(dst, fmTransferID, fm.TransferID)
	dst = appendString(dst, fmFileName, fm.FileName)
	dst = appendUint(dst, fmFileSize, uint64(max(fm.FileSize, 0)))
	dst = appendUint(dst, fmTotalChunks, uint64(max(fm.TotalChunks, 0)))
	dst = appendUint(dst, fmChunkIndex, uint64(max(fm.ChunkIndex, 0)))
	if fm.IsCode {
		dst = appendUint(dst, fmIsCode, 1)
	}
	dst = appendString(dst, fmChecksum, fm.Checksum)
	return dst
}

func decodeFileMeta(data []byte) (*FileMetadata, error) {
	fm := &FileMetadata{}
	err := walkTLV(data, func(tag byte, val []byte) error {
		switch tag {
		case fmTransferID:
			fm.TransferID = string(val)
		case fmFileName:
			fm.FileName = string(val)
		case fmFileSize:
			fm.FileSize = int64(clampUvarint(val, math.MaxInt64))
		case fmTotalChunks:
			fm.TotalChunks = int(clampUvarint(val, math.MaxInt32))
		case fmChunkIndex:
			fm.ChunkIndex = int(clampUvarint(val, math.MaxInt32))
		case fmIsCode:
			fm.IsCode = clampUvarint(val, 1) == 1
		case fmChecksum:
			fm.Checksum = string(val)
		}
		return nil
	})
	return fm, err
}

const maxWelcomePeers = 16

func encodePeers(dst []byte, peers []PeerSummary) []byte {
	var entry []byte
	for i := range peers {
		ps := &peers[i]
		entry = entry[:0]
		entry = appendString(entry, psID, ps.ID)
		entry = appendString(entry, psNickname, ps.Nickname)
		entry = appendString(entry, psAddr, ps.AddrStr)
		entry = appendUint(entry, psLocalPort, uint64(max(ps.LocalPort, 0)))
		var flags uint64
		if ps.IsMuted {
			flags |= flagMuted
		}
		if ps.IsDeafened {
			flags |= flagDeafened
		}
		if ps.IsSharingScreen {
			flags |= flagSharing
		}
		entry = appendUint(entry, psFlags, flags)
		entry = appendUint(entry, psVideoPort, uint64(max(ps.VideoPort, 0)))
		entry = appendUint(entry, psVideoFPS, uint64(max(ps.VideoFPS, 0)))
		dst = binary.AppendUvarint(dst, uint64(len(entry)))
		dst = append(dst, entry...)
	}
	return dst
}

func decodePeers(data []byte) ([]PeerSummary, error) {
	var peers []PeerSummary
	for len(data) > 0 {
		if len(peers) >= maxWelcomePeers {
			return nil, ErrMalformed
		}
		length, n := binary.Uvarint(data)
		if n <= 0 || length > uint64(len(data)-n) {
			return nil, ErrMalformed
		}
		entry := data[n : n+int(length)]
		data = data[n+int(length):]
		var ps PeerSummary
		err := walkTLV(entry, func(tag byte, val []byte) error {
			switch tag {
			case psID:
				ps.ID = string(val)
			case psNickname:
				ps.Nickname = string(val)
			case psAddr:
				ps.AddrStr = string(val)
			case psLocalPort:
				ps.LocalPort = int(clampUvarint(val, math.MaxUint16))
			case psFlags:
				flags := clampUvarint(val, math.MaxUint16)
				ps.IsMuted = flags&flagMuted != 0
				ps.IsDeafened = flags&flagDeafened != 0
				ps.IsSharingScreen = flags&flagSharing != 0
			case psVideoPort:
				ps.VideoPort = int(clampUvarint(val, math.MaxUint16))
			case psVideoFPS:
				ps.VideoFPS = int(clampUvarint(val, 1000))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		peers = append(peers, ps)
	}
	return peers, nil
}

// MaxSeqList bounds the number of sequence numbers in one NACK.
const MaxSeqList = 128

// AppendSeqList encodes ascending sequence numbers as a count followed by uvarint deltas.
func AppendSeqList(dst []byte, seqs []uint32) []byte {
	if len(seqs) > MaxSeqList {
		seqs = seqs[:MaxSeqList]
	}
	dst = binary.AppendUvarint(dst, uint64(len(seqs)))
	var prev uint32
	for i, s := range seqs {
		if i == 0 {
			dst = binary.AppendUvarint(dst, uint64(s))
		} else {
			dst = binary.AppendUvarint(dst, uint64(s-prev))
		}
		prev = s
	}
	return dst
}

// ParseSeqList decodes a list written by AppendSeqList.
func ParseSeqList(data []byte) ([]uint32, error) {
	n, k := binary.Uvarint(data)
	if k <= 0 || n > MaxSeqList {
		return nil, ErrMalformed
	}
	data = data[k:]
	out := make([]uint32, 0, n)
	var prev uint32
	for i := uint64(0); i < n; i++ {
		v, k := binary.Uvarint(data)
		if k <= 0 || v > math.MaxUint32 {
			return nil, ErrMalformed
		}
		data = data[k:]
		if i == 0 {
			prev = uint32(v)
		} else {
			prev += uint32(v)
		}
		out = append(out, prev)
	}
	return out, nil
}
