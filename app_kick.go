package main

import (
	"fmt"
	"strings"

	"github.com/thebanri/limoni-voice/internal/engine"
	"github.com/thebanri/limoni-voice/internal/p2p"
)

// matchPeer finds the member a command names: its exact nickname or ID, or else the only
// nickname starting with target. The second result explains a miss.
func matchPeer(peers []*p2p.PeerInfo, target string) (*p2p.PeerInfo, string) {
	target = strings.TrimPrefix(strings.TrimSpace(target), "@")
	if target == "" {
		return nil, "Name a member"
	}
	lower := strings.ToLower(target)
	var prefixed []*p2p.PeerInfo
	for _, p := range peers {
		if p.ID == target || strings.ToLower(p.Nickname) == lower {
			return p, ""
		}
		if strings.HasPrefix(strings.ToLower(p.Nickname), lower) {
			prefixed = append(prefixed, p)
		}
	}
	switch len(prefixed) {
	case 1:
		return prefixed[0], ""
	case 0:
		return nil, fmt.Sprintf("User '%s' not found", target)
	default:
		return nil, fmt.Sprintf("'%s' matches more than one member, type the full name", target)
	}
}

// kickMember removes the named member (/kick, /ban).
func (a *App) kickMember(target string, ban bool) {
	if !a.node.IsHost {
		a.room.SetToast("Only the room host can remove members")
		return
	}
	peer, why := matchPeer(a.node.GetPeersList(), target)
	if peer == nil {
		a.room.SetToast(why)
		return
	}
	if err := a.node.KickMember(peer.ID, ban); err != nil {
		a.room.SetToast(err.Error())
		return
	}
	if ban {
		a.room.SetToast(fmt.Sprintf("%s was banned from the room", peer.Nickname))
	} else {
		a.room.SetToast(fmt.Sprintf("%s was removed from the room", peer.Nickname))
	}
}

// onKicked runs on a network goroutine once the node left a room the host removed us from;
// the event loop moves back to the lobby.
func (a *App) onKicked(hostNick string, ban bool) {
	msg := fmt.Sprintf("%s removed you from the room", hostNick)
	if ban {
		msg = fmt.Sprintf("%s banned you from the room", hostNick)
	}
	a.kicked.Store(&msg)
	a.notifier.NotifyNow(notifyTitle, tr(msg))
}

// applyKicked returns to the lobby after a removal. Event loop only.
func (a *App) applyKicked() {
	msg := a.kicked.Swap(nil)
	if msg == nil || a.currentScreen != ScreenRoom {
		return
	}
	a.audio.PlaySound(engine.SoundLeave)
	a.resetToLobby()
	a.lobby.SetToast(*msg)
}
