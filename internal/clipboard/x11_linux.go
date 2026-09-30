//go:build linux

package clipboard

import (
	"errors"
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// x11Session is an X connection with a hidden window to take part in selections.
type x11Session struct {
	conn  *xgb.Conn
	win   xproto.Window
	atoms map[string]xproto.Atom
}

func openX11() (*x11Session, error) {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, err
	}
	s := &x11Session{conn: conn, atoms: map[string]xproto.Atom{}}
	for _, name := range append([]string{"CLIPBOARD", "TARGETS", "LIMONI_CLIP"}, textMimes...) {
		r, err := xproto.InternAtom(conn, false, uint16(len(name)), name).Reply()
		if err != nil {
			conn.Close()
			return nil, err
		}
		s.atoms[name] = r.Atom
	}
	if s.win, err = xproto.NewWindowId(conn); err == nil {
		screen := xproto.Setup(conn).DefaultScreen(conn)
		err = xproto.CreateWindowChecked(conn, screen.RootDepth, s.win, screen.Root,
			0, 0, 1, 1, 0, xproto.WindowClassInputOutput, screen.RootVisual, 0, nil).Check()
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	return s, nil
}

// readX11 asks the CLIPBOARD selection's owner for its text.
func readX11(timeout time.Duration) (string, error) {
	s, err := openX11()
	if err != nil {
		return "", err
	}
	defer s.conn.Close()
	owner, err := xproto.GetSelectionOwner(s.conn, s.atoms["CLIPBOARD"]).Reply()
	if err != nil || owner.Owner == xproto.WindowNone {
		return "", err
	}
	prop := s.atoms["LIMONI_CLIP"]
	for _, target := range []string{"UTF8_STRING", "STRING"} {
		xproto.ConvertSelection(s.conn, s.win, s.atoms["CLIPBOARD"], s.atoms[target], prop, xproto.TimeCurrentTime)
		ok, err := s.waitNotify(timeout)
		if err != nil {
			return "", err
		}
		if !ok {
			continue
		}
		r, err := xproto.GetProperty(s.conn, true, s.win, prop, xproto.GetPropertyTypeAny, 0, 1<<24).Reply()
		if err != nil {
			return "", err
		}
		if r.Format == 8 {
			return string(r.Value), nil
		}
	}
	return "", nil
}

// waitNotify waits for the SelectionNotify answering a ConvertSelection and reports
// whether the owner converted it.
func (s *x11Session) waitNotify(timeout time.Duration) (bool, error) {
	got := make(chan bool, 1)
	go func() {
		for {
			ev, err := s.conn.WaitForEvent()
			if ev == nil && err == nil {
				got <- false
				return
			}
			if sn, ok := ev.(xproto.SelectionNotifyEvent); ok {
				got <- sn.Property != xproto.AtomNone
				return
			}
		}
	}()
	select {
	case ok := <-got:
		return ok, nil
	case <-time.After(timeout):
		s.conn.Close() // unblocks the goroutine
		return false, errors.New("clipboard: X11 selection owner did not answer")
	}
}

// x11Owner is the connection serving the text this process copied.
var x11Owner struct {
	sync.Mutex
	conn *xgb.Conn
}

// writeX11 takes the CLIPBOARD selection and hands text to whoever pastes, until
// another client takes it over.
func writeX11(text string) error {
	s, err := openX11()
	if err != nil {
		return err
	}
	clipboard := s.atoms["CLIPBOARD"]
	xproto.SetSelectionOwner(s.conn, s.win, clipboard, xproto.TimeCurrentTime)
	owner, err := xproto.GetSelectionOwner(s.conn, clipboard).Reply()
	if err != nil || owner.Owner != s.win {
		s.conn.Close()
		return errors.New("clipboard: could not own the X11 clipboard")
	}

	x11Owner.Lock()
	if x11Owner.conn != nil {
		x11Owner.conn.Close()
	}
	x11Owner.conn = s.conn
	x11Owner.Unlock()

	targets := []xproto.Atom{s.atoms["TARGETS"]}
	isText := map[xproto.Atom]bool{}
	for _, mime := range textMimes {
		targets = append(targets, s.atoms[mime])
		isText[s.atoms[mime]] = true
	}
	targetBytes := make([]byte, 4*len(targets))
	for i, a := range targets {
		xgb.Put32(targetBytes[4*i:], uint32(a))
	}

	go func() {
		defer func() {
			x11Owner.Lock()
			if x11Owner.conn == s.conn {
				x11Owner.conn = nil
			}
			x11Owner.Unlock()
			s.conn.Close()
		}()
		for {
			ev, err := s.conn.WaitForEvent()
			if ev == nil && err == nil {
				return
			}
			switch e := ev.(type) {
			case xproto.SelectionClearEvent:
				return
			case xproto.SelectionRequestEvent:
				prop := e.Property
				if prop == xproto.AtomNone {
					prop = e.Target // obsolete clients
				}
				switch {
				case e.Target == s.atoms["TARGETS"]:
					xproto.ChangeProperty(s.conn, xproto.PropModeReplace, e.Requestor, prop,
						xproto.AtomAtom, 32, uint32(len(targets)), targetBytes)
				case isText[e.Target]:
					xproto.ChangeProperty(s.conn, xproto.PropModeReplace, e.Requestor, prop,
						e.Target, 8, uint32(len(text)), []byte(text))
				default:
					prop = xproto.AtomNone // refuse
				}
				notify := xproto.SelectionNotifyEvent{
					Time: e.Time, Requestor: e.Requestor, Selection: e.Selection,
					Target: e.Target, Property: prop,
				}
				xproto.SendEvent(s.conn, false, e.Requestor, 0, string(notify.Bytes()))
			}
		}
	}()
	return nil
}
