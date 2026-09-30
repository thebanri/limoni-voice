//go:build linux

// Package clipboard reads and owns the desktop clipboard natively, speaking the Wayland
// data-control protocol and X11 selections directly, so no wl-clipboard, xclip or xsel
// has to be installed.
package clipboard

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// textMimes are the text types offered when copying, and tried in order when pasting.
var textMimes = []string{"text/plain;charset=utf-8", "UTF8_STRING", "text/plain", "TEXT", "STRING"}

var errNoDataControl = errors.New("clipboard: compositor has no data-control protocol")

// wlConn is a minimal Wayland client connection: wire encoding plus fd passing.
type wlConn struct {
	sock   *net.UnixConn
	nextID uint32
	in     []byte
	fds    []int
}

func dialWayland() (*wlConn, error) {
	name := os.Getenv("WAYLAND_DISPLAY")
	if name == "" {
		return nil, errors.New("clipboard: WAYLAND_DISPLAY not set")
	}
	if !filepath.IsAbs(name) {
		name = filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), name)
	}
	sock, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: name, Net: "unix"})
	if err != nil {
		return nil, err
	}
	return &wlConn{sock: sock, nextID: 2}, nil // 1 is wl_display
}

func (c *wlConn) Close() {
	for _, fd := range c.fds {
		syscall.Close(fd)
	}
	c.fds = nil
	c.sock.Close()
}

func (c *wlConn) newID() uint32 {
	id := c.nextID
	c.nextID++
	return id
}

// wlArgs builds a request body.
type wlArgs []byte

func (a wlArgs) u32(v uint32) wlArgs { return binary.LittleEndian.AppendUint32(a, v) }

func (a wlArgs) str(s string) wlArgs {
	a = a.u32(uint32(len(s) + 1))
	a = append(a, s...)
	a = append(a, 0)
	for len(a)%4 != 0 {
		a = append(a, 0)
	}
	return a
}

// send writes one request, passing fd along with it when fd >= 0.
func (c *wlConn) send(obj uint32, op uint16, args wlArgs, fd int) error {
	msg := make([]byte, 8, 8+len(args))
	binary.LittleEndian.PutUint32(msg, obj)
	binary.LittleEndian.PutUint32(msg[4:], uint32(8+len(args))<<16|uint32(op))
	msg = append(msg, args...)
	var oob []byte
	if fd >= 0 {
		oob = syscall.UnixRights(fd)
	}
	_, _, err := c.sock.WriteMsgUnix(msg, oob, nil)
	return err
}

// wlEvent is one decoded event; body holds its arguments.
type wlEvent struct {
	obj  uint32
	op   uint16
	body []byte
}

func (e wlEvent) u32(i int) uint32 {
	if len(e.body) < i+4 {
		return 0
	}
	return binary.LittleEndian.Uint32(e.body[i:])
}

// str reads the string argument at byte offset i and returns it with the offset after it.
func (e wlEvent) str(i int) (string, int) {
	n := int(e.u32(i))
	if n == 0 || len(e.body) < i+4+n {
		return "", i + 4
	}
	s := string(e.body[i+4 : i+4+n-1])
	return s, i + 4 + (n+3)&^3
}

// next reads the next event, waiting at most until deadline.
func (c *wlConn) next(deadline time.Time) (wlEvent, error) {
	for {
		if len(c.in) >= 8 {
			size := int(binary.LittleEndian.Uint32(c.in[4:]) >> 16)
			if size < 8 {
				return wlEvent{}, errors.New("clipboard: bad wayland message")
			}
			if len(c.in) >= size {
				ev := wlEvent{
					obj:  binary.LittleEndian.Uint32(c.in),
					op:   uint16(binary.LittleEndian.Uint32(c.in[4:])),
					body: append([]byte(nil), c.in[8:size]...),
				}
				c.in = c.in[size:]
				if ev.obj == 1 && ev.op == 0 { // wl_display.error
					msg, _ := ev.str(8)
					return ev, fmt.Errorf("clipboard: wayland error %d: %s", ev.u32(4), msg)
				}
				return ev, nil
			}
		}
		c.sock.SetReadDeadline(deadline)
		buf := make([]byte, 4096)
		oob := make([]byte, syscall.CmsgSpace(28*4))
		n, oobn, _, _, err := c.sock.ReadMsgUnix(buf, oob)
		if oobn > 0 {
			if msgs, perr := syscall.ParseSocketControlMessage(oob[:oobn]); perr == nil {
				for _, m := range msgs {
					if fds, ferr := syscall.ParseUnixRights(&m); ferr == nil {
						c.fds = append(c.fds, fds...)
					}
				}
			}
		}
		if err != nil {
			return wlEvent{}, err
		}
		if n == 0 {
			return wlEvent{}, errors.New("clipboard: wayland connection closed")
		}
		c.in = append(c.in, buf[:n]...)
	}
}

// takeFD pops the oldest file descriptor received with an event.
func (c *wlConn) takeFD() int {
	if len(c.fds) == 0 {
		return -1
	}
	fd := c.fds[0]
	c.fds = c.fds[1:]
	return fd
}

// wlDevice is a bound data-control device on the first seat.
type wlDevice struct {
	*wlConn
	manager, device uint32
	offers          map[uint32][]string // data offers and their mime types
	selection       uint32              // current clipboard offer, 0 if none
}

// openDevice binds the data-control manager and seat and returns the device with the
// current selection already received.
func openDevice(deadline time.Time) (*wlDevice, error) {
	c, err := dialWayland()
	if err != nil {
		return nil, err
	}
	d := &wlDevice{wlConn: c, offers: map[uint32][]string{}}
	registry := c.newID()
	if err := c.send(1, 1, wlArgs{}.u32(registry), -1); err != nil { // wl_display.get_registry
		c.Close()
		return nil, err
	}
	type global struct {
		name  uint32
		iface string
	}
	var manager, seat *global
	err = d.roundtrip(deadline, func(ev wlEvent) {
		if ev.obj != registry || ev.op != 0 { // wl_registry.global
			return
		}
		iface, _ := ev.str(4)
		g := &global{ev.u32(0), iface}
		switch iface {
		case "ext_data_control_manager_v1":
			manager = g
		case "zwlr_data_control_manager_v1":
			if manager == nil {
				manager = g
			}
		case "wl_seat":
			if seat == nil {
				seat = g
			}
		}
	})
	if err != nil {
		c.Close()
		return nil, err
	}
	if manager == nil || seat == nil {
		c.Close()
		return nil, errNoDataControl
	}
	bind := func(g *global) (uint32, error) {
		id := c.newID()
		return id, c.send(registry, 0, wlArgs{}.u32(g.name).str(g.iface).u32(1).u32(id), -1)
	}
	seatID, err := bind(seat)
	if err == nil {
		d.manager, err = bind(manager)
	}
	if err == nil {
		d.device = c.newID()
		err = c.send(d.manager, 1, wlArgs{}.u32(d.device).u32(seatID), -1) // get_data_device
	}
	if err == nil {
		err = d.roundtrip(deadline, nil)
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	return d, nil
}

// handle tracks offers and the selection; the rest goes to other.
func (d *wlDevice) handle(ev wlEvent, other func(wlEvent)) {
	switch {
	case ev.obj == d.device && ev.op == 0: // data_offer
		d.offers[ev.u32(0)] = nil
	case ev.obj == d.device && ev.op == 1: // selection
		if old := d.selection; old != 0 && old != ev.u32(0) {
			d.destroyOffer(old)
		}
		d.selection = ev.u32(0)
	case d.offers != nil && ev.op == 0 && d.isOffer(ev.obj): // offer.offer(mime)
		mime, _ := ev.str(0)
		d.offers[ev.obj] = append(d.offers[ev.obj], mime)
	default:
		if other != nil {
			other(ev)
		}
	}
}

func (d *wlDevice) isOffer(id uint32) bool {
	_, ok := d.offers[id]
	return ok
}

func (d *wlDevice) destroyOffer(id uint32) {
	delete(d.offers, id)
	d.send(id, 1, nil, -1)
}

// roundtrip dispatches events until the server has handled everything sent so far.
func (d *wlDevice) roundtrip(deadline time.Time, other func(wlEvent)) error {
	cb := d.newID()
	if err := d.send(1, 0, wlArgs{}.u32(cb), -1); err != nil { // wl_display.sync
		return err
	}
	for {
		ev, err := d.next(deadline)
		if err != nil {
			return err
		}
		if ev.obj == cb && ev.op == 0 {
			return nil
		}
		d.handle(ev, other)
	}
}

// readWayland returns the clipboard's text.
func readWayland(timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	d, err := openDevice(deadline)
	if err != nil {
		return "", err
	}
	defer d.Close()
	if d.selection == 0 {
		return "", nil
	}
	mime := ""
	for _, want := range textMimes {
		for _, have := range d.offers[d.selection] {
			if have == want && mime == "" {
				mime = want
			}
		}
	}
	if mime == "" {
		return "", nil // not text (an image, files…)
	}
	var p [2]int
	if err := syscall.Pipe2(p[:], syscall.O_CLOEXEC|syscall.O_NONBLOCK); err != nil {
		return "", err
	}
	r := os.NewFile(uintptr(p[0]), "clipboard")
	defer r.Close()
	err = d.send(d.selection, 0, wlArgs{}.str(mime), p[1]) // offer.receive
	syscall.Close(p[1])
	if err != nil {
		return "", err
	}
	// The source writes and closes its end; read until EOF.
	r.SetReadDeadline(deadline)
	var out []byte
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		out = append(out, buf[:n]...)
		if err == io.EOF {
			return string(out), nil
		}
		if err != nil {
			return string(out), err
		}
	}
}

// waylandOwner keeps the connection that owns the clipboard alive for the copied text.
var waylandOwner struct {
	sync.Mutex
	conn *wlConn
}

// writeWayland makes this process the clipboard owner of text, serving pastes in the
// background until something else is copied.
func writeWayland(text string) error {
	d, err := openDevice(time.Now().Add(2 * time.Second))
	if err != nil {
		return err
	}
	source := d.newID()
	err = d.send(d.manager, 0, wlArgs{}.u32(source), -1) // create_data_source
	for _, mime := range textMimes {
		if err == nil {
			err = d.send(source, 0, wlArgs{}.str(mime), -1) // source.offer
		}
	}
	if err == nil {
		err = d.send(d.device, 0, wlArgs{}.u32(source), -1) // set_selection
	}
	if err == nil {
		err = d.roundtrip(time.Now().Add(2*time.Second), nil)
	}
	if err != nil {
		d.Close()
		return err
	}

	waylandOwner.Lock()
	if old := waylandOwner.conn; old != nil {
		old.sock.Close()
	}
	waylandOwner.conn = d.wlConn
	waylandOwner.Unlock()

	go func() {
		defer func() {
			waylandOwner.Lock()
			if waylandOwner.conn == d.wlConn {
				waylandOwner.conn = nil
			}
			waylandOwner.Unlock()
			d.Close()
		}()
		for {
			ev, err := d.next(time.Time{})
			if err != nil {
				return
			}
			d.handle(ev, func(ev wlEvent) {
				if ev.obj != source {
					return
				}
				switch ev.op {
				case 0: // send(mime, fd)
					if fd := d.takeFD(); fd >= 0 {
						go func() {
							f := os.NewFile(uintptr(fd), "clipboard")
							f.WriteString(text)
							f.Close()
						}()
					}
				case 1: // cancelled: something else was copied
					d.sock.Close()
				}
			})
		}
	}()
	return nil
}
