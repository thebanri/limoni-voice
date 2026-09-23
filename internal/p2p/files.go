package p2p

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thebanri/limoni-voice/internal/protocol"
)

// SendFile streams a local file to all peers in the room with E2EE chunking
func (n *P2PNode) SendFile(filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	return n.SendFileBytes(filepath.Base(filePath), data, false)
}

// SendCodeSnippet shares a syntax code snippet with peers in the room
func (n *P2PNode) SendCodeSnippet(title string, codeContent string) error {
	if title == "" {
		title = "snippet.txt"
	}
	return n.SendFileBytes(title, []byte(codeContent), true)
}

// SendFileBytes streams raw file or code bytes to all peers with E2EE chunking
func (n *P2PNode) SendFileBytes(fileName string, data []byte, isCode bool) error {
	n.mu.RLock()
	if !n.IsConnected || len(n.Peers) == 0 {
		n.mu.RUnlock()
		return errors.New("cannot transfer: not connected to room or no peers")
	}
	room := n.roomID
	senderID := n.LocalID
	nickname := n.Nickname
	n.mu.RUnlock()

	if len(data) > MaxFileTransferSize {
		return fmt.Errorf("file exceeds %d MB limit", MaxFileTransferSize/(1024*1024))
	}

	fileSum := sha256.Sum256([]byte(fileName))
	transferID := fmt.Sprintf("tf_%d_%x", time.Now().UnixNano(), fileSum[:4])
	const chunkSize = 16384 // 16 KB per chunk
	fileSize := int64(len(data))
	totalChunks := max((len(data)+chunkSize-1)/chunkSize, 1)

	h := sha256.Sum256(data)
	checksum := hex.EncodeToString(h[:])

	headerPkt := P2PPacket{
		Type:     PacketFileHeader,
		RoomCode: room,
		SenderID: senderID,
		Nickname: nickname,
		FileMeta: &FileMetadata{
			TransferID:  transferID,
			FileName:    fileName,
			FileSize:    fileSize,
			TotalChunks: totalChunks,
			IsCode:      isCode,
			Checksum:    checksum,
		},
		Timestamp: time.Now().UnixMilli(),
	}
	n.sendRedundant(&headerPkt, protocol.FrameReliable)

	go func() {
		startTime := time.Now()
		var sentBytes int64

		for i := 0; i < totalChunks; i++ {
			start := i * chunkSize
			end := min(start+chunkSize, len(data))
			chunkData := data[start:end]
			sentBytes += int64(len(chunkData))

			chunkPkt := P2PPacket{
				Type:     PacketFileChunk,
				RoomCode: room,
				SenderID: senderID,
				Nickname: nickname,
				FileMeta: &FileMetadata{
					TransferID:  transferID,
					FileName:    fileName,
					FileSize:    fileSize,
					TotalChunks: totalChunks,
					ChunkIndex:  i,
					IsCode:      isCode,
					Checksum:    checksum,
				},
				Payload:   chunkData,
				Timestamp: time.Now().UnixMilli(),
			}
			n.sendRedundant(&chunkPkt, protocol.FrameReliable)

			elapsed := time.Since(startTime).Seconds()
			var speed float64
			if elapsed > 0.05 {
				speed = float64(sentBytes) / elapsed
			}

			isDone := i == totalChunks-1
			if n.OnFileTransferProgress != nil {
				n.OnFileTransferProgress(transferID, fileName, sentBytes, fileSize, speed, true, isDone, nil)
			}
			time.Sleep(6 * time.Millisecond) // smooth pacing
		}
	}()

	return nil
}

// handleFilePacketLocked processes file header and chunk packets. Caller holds n.mu.
func (n *P2PNode) handleFilePacketLocked(pkt *P2PPacket) {
	meta := pkt.FileMeta
	if meta == nil || meta.TransferID == "" {
		return
	}

	if pkt.Type == PacketFileHeader {
		// DoS Protection: Size and chunk count limits
		if meta.FileSize <= 0 || meta.FileSize > MaxFileTransferSize || meta.TotalChunks <= 0 || meta.TotalChunks > MaxFileChunks {
			n.log(fmt.Sprintf("[SECURITY] Rejected file transfer %s: size %d bytes or %d chunks exceeds limit", meta.TransferID, meta.FileSize, meta.TotalChunks))
			return
		}
		if n.incomingTransfers == nil {
			n.incomingTransfers = make(map[string]*IncomingFileTransfer)
		}
		if _, dup := n.incomingTransfers[meta.TransferID]; dup {
			return // duplicate header over a redundant path
		}

		now := time.Now()
		for id, tr := range n.incomingTransfers {
			if now.Sub(tr.StartTime) > TransferExpiryDuration {
				delete(n.incomingTransfers, id)
			}
		}
		if len(n.incomingTransfers) >= MaxConcurrentTransfers {
			n.log("[SECURITY] Rejected file transfer: max concurrent incoming transfers reached")
			return
		}

		safeName := sanitizeFilename(meta.FileName, meta.IsCode)
		n.incomingTransfers[meta.TransferID] = &IncomingFileTransfer{
			TransferID:  meta.TransferID,
			FileName:    safeName,
			FileSize:    meta.FileSize,
			TotalChunks: meta.TotalChunks,
			Chunks:      make(map[int][]byte),
			StartTime:   time.Now(),
			IsCode:      meta.IsCode,
			Checksum:    meta.Checksum,
		}
		if n.OnFileTransferProgress != nil {
			go n.OnFileTransferProgress(meta.TransferID, safeName, 0, meta.FileSize, 0, false, false, nil)
		}
		return
	}

	if n.incomingTransfers == nil {
		return
	}
	transfer, exists := n.incomingTransfers[meta.TransferID]
	if !exists {
		return // Reject chunks for non-existent or expired transfers
	}
	if meta.ChunkIndex < 0 || meta.ChunkIndex >= transfer.TotalChunks {
		return
	}
	if len(pkt.Payload) > 65536 {
		return
	}
	if _, already := transfer.Chunks[meta.ChunkIndex]; !already {
		transfer.Chunks[meta.ChunkIndex] = bytes.Clone(pkt.Payload)
		transfer.Received += int64(len(pkt.Payload))
	}
	if transfer.Received > transfer.FileSize {
		delete(n.incomingTransfers, meta.TransferID)
		n.log(fmt.Sprintf("[SECURITY] Rejected file transfer %s: more data than announced", meta.TransferID))
		return
	}

	elapsed := time.Since(transfer.StartTime).Seconds()
	var speed float64
	if elapsed > 0.05 {
		speed = float64(transfer.Received) / elapsed
	}

	isDone := len(transfer.Chunks) >= transfer.TotalChunks
	if !isDone {
		if n.OnFileTransferProgress != nil {
			go n.OnFileTransferProgress(meta.TransferID, transfer.FileName, transfer.Received, transfer.FileSize, speed, false, false, nil)
		}
		return
	}

	delete(n.incomingTransfers, meta.TransferID)
	senderID := pkt.SenderID
	senderNick := pkt.Nickname
	if peer, ok := n.Peers[senderID]; ok && peer.Nickname != "" {
		senderNick = peer.Nickname
	}

	// Assemble and verify in the background so audio / ping handling is not stalled.
	go func() {
		var assembled bytes.Buffer
		for idx := 0; idx < transfer.TotalChunks; idx++ {
			assembled.Write(transfer.Chunks[idx])
		}
		fullData := assembled.Bytes()

		if transfer.Checksum != "" {
			sum := sha256.Sum256(fullData)
			if hex.EncodeToString(sum[:]) != transfer.Checksum {
				if n.OnFileTransferProgress != nil {
					n.OnFileTransferProgress(transfer.TransferID, transfer.FileName, transfer.Received, transfer.FileSize, 0, false, true, fmt.Errorf("checksum mismatch (corrupted or tampered)"))
				}
				return
			}
		}

		offer := &FileOffer{
			TransferID: transfer.TransferID,
			SenderID:   senderID,
			SenderNick: senderNick,
			FileName:   transfer.FileName,
			FileSize:   int64(len(fullData)),
			IsCode:     transfer.IsCode,
			Checksum:   transfer.Checksum,
			Data:       fullData,
		}

		if n.OnFileOfferReceived != nil {
			n.OnFileOfferReceived(offer)
		} else if n.OnFileReceived != nil {
			if savedPath, err := SaveAcceptedFile(offer); err == nil {
				n.OnFileReceived(transfer.TransferID, transfer.FileName, savedPath, transfer.IsCode, string(fullData))
			}
		}

		if n.OnFileTransferProgress != nil {
			n.OnFileTransferProgress(transfer.TransferID, transfer.FileName, int64(len(fullData)), transfer.FileSize, speed, false, true, nil)
		}
	}()
}

// GetLimoniTransfersDir returns the cross-platform path to ~/Downloads/LimoniTransfers/
func GetLimoniTransfersDir() string {
	if testing.Testing() {
		return filepath.Join(os.TempDir(), "LimoniTransfers_test")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		if runtime.GOOS == "windows" {
			homeDir = os.Getenv("USERPROFILE")
			if homeDir == "" {
				homeDir = os.Getenv("HOMEDRIVE") + os.Getenv("HOMEPATH")
			}
		} else {
			homeDir = os.Getenv("HOME")
		}
	}
	if homeDir == "" {
		homeDir = "."
	}
	return filepath.Join(homeDir, "Downloads", "LimoniTransfers")
}

// SaveAcceptedFile saves an accepted file transfer or code snippet to Downloads/LimoniTransfers.
func SaveAcceptedFile(offer *FileOffer) (string, error) {
	if offer == nil || len(offer.Data) == 0 {
		return "", errors.New("empty file offer data")
	}

	dlDir := GetLimoniTransfersDir()
	if err := os.MkdirAll(dlDir, 0755); err != nil {
		return "", err
	}

	safeName := sanitizeFilename(offer.FileName, offer.IsCode)
	destPath := filepath.Join(dlDir, safeName)
	if _, err := os.Stat(destPath); err == nil {
		ext := filepath.Ext(safeName)
		base := strings.TrimSuffix(safeName, ext)
		for i := 1; i < 1000; i++ {
			altPath := filepath.Join(dlDir, fmt.Sprintf("%s (%d)%s", base, i, ext))
			if _, err := os.Stat(altPath); os.IsNotExist(err) {
				destPath = altPath
				break
			}
		}
	}

	if err := os.WriteFile(destPath, offer.Data, 0644); err != nil {
		return "", err
	}
	return destPath, nil
}
