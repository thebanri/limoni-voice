package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/core/driver"
	"github.com/thebanri/limoni/core/terminal"
	"github.com/thebanri/limoni/widgets"
)

// Knock-to-join, host side: with P2PNode.KnockToJoin on, every joiner that proved the room
// key waits here until the host lets it in or turns it away. Requests the host leaves
// unanswered are declined when the joiner would have given up anyway (knockWindow).

type knockRequest struct {
	id, nickname string
	at           time.Time
}

type knockQueue struct {
	mu       sync.Mutex
	requests []knockRequest
}

func (q *knockQueue) add(id, nickname string, now time.Time) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, r := range q.requests {
		if r.id == id {
			return false
		}
	}
	q.requests = append(q.requests, knockRequest{id: id, nickname: nickname, at: now})
	return true
}

// front returns the oldest request still inside its window and the requests that expired.
func (q *knockQueue) front(now time.Time) (active *knockRequest, expired []knockRequest) {
	q.mu.Lock()
	defer q.mu.Unlock()
	kept := q.requests[:0]
	for _, r := range q.requests {
		if now.Sub(r.at) >= knockWindow {
			expired = append(expired, r)
		} else {
			kept = append(kept, r)
		}
	}
	q.requests = kept
	if len(q.requests) > 0 {
		r := q.requests[0]
		active = &r
	}
	return active, expired
}

func (q *knockQueue) pop() (knockRequest, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.requests) == 0 {
		return knockRequest{}, false
	}
	r := q.requests[0]
	q.requests = q.requests[1:]
	return r, true
}

func (q *knockQueue) clear() {
	q.mu.Lock()
	q.requests = nil
	q.mu.Unlock()
}

// onKnock runs when a verified joiner waits for the host's decision.
func (a *App) onKnock(joinerID, nickname string) {
	if nickname == "" {
		nickname = "Someone"
	}
	if !a.knocks.add(joinerID, nickname, time.Now()) {
		return
	}
	a.audio.PlaySound(SoundJoin)
	a.room.AddLog(fmt.Sprintf("[ROOM] %s is knocking: [Y] let in, [N] turn away", nickname))
	a.notifier.NotifyNow(notifyTitle, nickname+" wants to join the room")
}

// activeKnock returns the request to show, declining the ones that ran out of time.
func (a *App) activeKnock() *knockRequest {
	active, expired := a.knocks.front(time.Now())
	for _, r := range expired {
		a.node.ApproveJoin(r.id, false)
		a.room.AddLog(fmt.Sprintf("[ROOM] %s was turned away (no answer)", r.nickname))
	}
	return active
}

func (a *App) answerKnock(allow bool) {
	r, ok := a.knocks.pop()
	if !ok {
		return
	}
	if !a.node.ApproveJoin(r.id, allow) {
		a.room.AddLog(fmt.Sprintf("[ROOM] %s stopped waiting", r.nickname))
		return
	}
	if allow {
		a.room.AddLog(fmt.Sprintf("[ROOM] Let %s in", r.nickname))
	} else {
		a.room.AddLog(fmt.Sprintf("[ROOM] Turned %s away", r.nickname))
	}
}

func (a *App) handleKnockKey(e driver.KeyEvent) {
	switch {
	case e.Type == driver.KeyEnter, e.Type == driver.KeyRune && (e.Ch == 'y' || e.Ch == 'Y'):
		a.answerKnock(true)
	case e.Type == driver.KeyEsc, e.Type == driver.KeyRune && (e.Ch == 'n' || e.Ch == 'N'):
		a.answerKnock(false)
	}
}

// toggleKnock switches knock-to-join for the room this node hosts.
func (a *App) toggleKnock() {
	if !a.node.IsHost {
		a.room.SetToast("Only the room host can change who gets in")
		return
	}
	a.node.mu.Lock()
	a.node.KnockToJoin = !a.node.KnockToJoin
	on := a.node.KnockToJoin
	a.node.mu.Unlock()
	if on {
		a.room.SetToast("Knock to join ON: you let each new member in")
		a.room.AddLog("[ROOM] Knock to join ON: people with the room key wait until you let them in")
	} else {
		a.knocks.clear()
		a.room.SetToast("Knock to join OFF")
		a.room.AddLog("[ROOM] Knock to join OFF: the room key is enough to get in")
	}
}

// DrawKnockModal asks the host whether to let a waiting joiner in.
func DrawKnockModal(frame *terminal.Frame, screenArea cell.Rect, r *knockRequest, remaining time.Duration, onAllow, onDeny func()) {
	if r == nil {
		return
	}
	modalW, modalH := uint16(52), uint16(7)
	if screenArea.Width < modalW+2 {
		modalW = screenArea.Width - 2
	}
	if screenArea.Height < modalH+2 {
		return
	}
	area := terminal.CenterRect(screenArea, modalW, modalH)
	theme := CurrentTheme()
	buf := frame.Buffer
	widgets.DrawShadow(buf, area, 2, 1)
	openModal(frame, "knock_dialog", area, onDeny)
	defer frame.EndLayer()
	for y := area.Y; y < area.Y+area.Height; y++ {
		for x := area.X; x < area.X+area.Width; x++ {
			buf.SetCell(x, y, cell.Cell{Content: ' ', Style: cell.Style{Bg: theme.SurfaceBg}})
		}
	}
	block := widgets.Block{
		Title:          " 🔔 KNOCK KNOCK ",
		TitleAlignment: widgets.AlignCenter,
		Borders:        widgets.BorderAll,
		BorderSymbols:  widgets.SymbolsRounded,
		BorderStyle:    cell.Style{Fg: theme.Warning, Modifier: cell.ModifierBold},
		Style:          cell.Style{Bg: theme.SurfaceBg},
	}
	frame.RenderWidget(block, area)
	inner := block.Inner(area)
	if inner.Height < 4 || inner.Width < 20 {
		return
	}
	name := []rune(r.nickname)
	if max := int(inner.Width) - 24; len(name) > max && max > 1 {
		name = append(name[:max-1], '…')
	}
	buf.SetString(inner.X+1, inner.Y+1, string(name)+" wants to join the room", cell.Style{Fg: theme.Text, Bg: theme.SurfaceBg, Modifier: cell.ModifierBold})
	secs := int(remaining.Round(time.Second) / time.Second)
	buf.SetString(inner.X+1, inner.Y+2, fmt.Sprintf("They know the room key. Turned away in %ds.", max(secs, 0)), cell.Style{Fg: theme.TextMuted, Bg: theme.SurfaceBg})

	allow := " [Y] Let in "
	deny := " [N] Turn away "
	allowX := inner.X + 1
	denyX := allowX + uint16(len([]rune(allow))) + 2
	buf.SetString(allowX, inner.Y+4, allow, cell.Style{Fg: cell.NewColorRGB(0, 0, 0), Bg: theme.Success, Modifier: cell.ModifierBold})
	buf.SetString(denyX, inner.Y+4, deny, cell.Style{Fg: cell.NewColorRGB(0xFF, 0xFF, 0xFF), Bg: theme.Danger, Modifier: cell.ModifierBold})
	frame.RegisterClickHandler(cell.NewRect(allowX, inner.Y+4, uint16(len([]rune(allow))), 1), func(_ driver.MouseEvent) { onAllow() })
	frame.RegisterClickHandler(cell.NewRect(denyX, inner.Y+4, uint16(len([]rune(deny))), 1), func(_ driver.MouseEvent) { onDeny() })
}
