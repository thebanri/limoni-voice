package terminal

import (
	"image"

	"github.com/thebanri/limoni/core/buffer"
	"github.com/thebanri/limoni/core/cell"
	"github.com/thebanri/limoni/graphics"
)

// kittyImages is what kitty has been sent. Each picture is sent once, under
// an id of its own, and shown with placements. A picture moving, or
// scrolling half out of view (a graphics.Clip), costs a placement of a few
// dozen bytes instead of the whole picture again. One no longer shown is
// freed in kitty.
//
// Each placement keeps an id of its own, tied to the cells it covers, and a
// frame changes only the placements that differ from the last frame's.
// Deleting every placement and making them all again whenever any picture
// changed cost about 58 KB a frame for a screen of a thousand small
// pictures, and Konsole showed a flash of the empty screen between the
// two.
type kittyImages struct {
	ids   map[kittyKey]*kittyEntry
	next  uint32
	stamp uint64
	// gen is graphics.Generation when the pictures were sent; a ForgetImage
	// since means one may have been rewritten in place.
	gen uint64
	// stale is set when the terminal may have none of them (a resume).
	stale bool

	// placed is what is on screen, by the cells each placement covers. It
	// holds placements by value: a tile that moves is a new key each frame,
	// and a pointer to each was an allocation per tile per frame.
	placed  map[placeKey]kittyPlaced
	nextPID uint32
	// seen counts the pictures on each area in the frame being placed.
	seen map[cell.Rect]int
	// moved is the regions whose cells had no placement of their picture,
	// to place once the spare placements are known.
	moved []kittyMove
	// spare is the placements free to move this frame. spareAt is the
	// first spare of each picture not yet taken, and each spare's next the
	// one after it.
	spare   []kittySpare
	spareAt map[uint32]int
}

// kittyMove is a region to place with a spare placement or a new one.
type kittyMove struct {
	pk   placeKey
	want kittyPlaced
}

// kittySpare is a placement no longer on its cells, pk.
type kittySpare struct {
	pk   placeKey
	p    kittyPlaced
	next int // -1 for none
}

// placeKey names a placement by its cells. n tells apart pictures that
// share their cells, in the order the frame gives them.
type placeKey struct {
	area cell.Rect
	n    int
}

// kittyKey is a picture as sent: kitty is sent it scaled to its cells.
type kittyKey struct {
	img          image.Image
	cols, rows   uint16
	cellW, cellH uint16
	transparent  bool
}

type kittyEntry struct {
	id    uint32
	stamp uint64
}

// kittyPlaced is one placement on screen: picture id, under placement id
// pid, showing the source rectangle x, y, w, h at depth z.
type kittyPlaced struct {
	id, pid    uint32
	x, y, w, h int
	z          int
	stamp      uint64
}

// place appends what shows regions: new pictures, then the placements that
// changed, then the deletion of placements and pictures no longer shown.
// With redraw set, as after the screen was cleared or a layer changed,
// every placement is made again.
//
// A placement whose cells now show another picture, or no picture, is
// spare: a picture placed on new cells takes a spare placement of the same
// picture and moves it in one step. Only the spares left over are deleted,
// after everything is placed, so nothing goes missing in between. Tiles
// moving to new cells cost a placement each, not a placement and a
// deletion.
func (k *kittyImages) place(dst []byte, regions []ImageRegion, cellW, cellH uint16, redraw bool) []byte {
	if gen := graphics.Generation(); k.stale || gen != k.gen {
		dst = k.freeAll(dst)
		k.gen, k.stale = gen, false
	}
	if k.ids == nil {
		k.ids = make(map[kittyKey]*kittyEntry)
	}
	if k.placed == nil {
		k.placed = make(map[placeKey]kittyPlaced)
		k.seen = make(map[cell.Rect]int)
		k.spareAt = make(map[uint32]int)
	}
	clear(k.seen)
	if redraw {
		dst = append(dst, graphics.KittyDeletePlacements...)
		clear(k.placed)
	}
	k.stamp++
	k.moved = k.moved[:0]
	for _, reg := range regions {
		pk := k.placeKey(reg.Area)
		key, x, y, w, h := kittyPicture(reg, cellW, cellH)
		e := k.ids[key]
		if e == nil {
			k.next++
			e = &kittyEntry{id: 0x4c690000 + k.next}
			seq := graphics.EncodeKittyTransmit(key.img, key.cols, key.rows, cellW, cellH, e.id, reg.Transparent)
			if seq == "" {
				continue
			}
			dst = append(dst, seq...)
			k.ids[key] = e
		}
		e.stamp = k.stamp
		z := reg.ZIndex
		if z == 0 {
			z = -1
		}

		want := kittyPlaced{id: e.id, x: x, y: y, w: w, h: h, z: z, stamp: k.stamp}
		p, ok := k.placed[pk]
		if !ok || p.id != e.id {
			// Placed below, once the spare placements are known.
			k.moved = append(k.moved, kittyMove{pk, want})
			continue
		}
		want.pid, p.stamp = p.pid, k.stamp
		k.placed[pk] = want
		if want != p {
			dst = k.appendPlace(dst, pk, want)
		}
	}

	// Placements whose cells now show another picture, or none, are spare.
	k.spare = k.spare[:0]
	for pk, p := range k.placed {
		if p.stamp != k.stamp {
			k.spare = append(k.spare, kittySpare{pk, p, -1})
		}
	}
	clear(k.spareAt)
	if len(k.moved) > 0 {
		for i := range k.spare {
			if head, ok := k.spareAt[k.spare[i].p.id]; ok {
				k.spare[i].next = head
			}
			k.spareAt[k.spare[i].p.id] = i
		}
	}
	for _, m := range k.moved {
		if p, ok := k.takeSpare(m.want.id); ok {
			m.want.pid = p.pid
		} else {
			k.nextPID++
			m.want.pid = k.nextPID
		}
		k.placed[m.pk] = m.want
		dst = k.appendPlace(dst, m.pk, m.want)
	}
	// Every spare leaves its old cells, unless a picture was placed there
	// this frame, and the spares no picture took are deleted, after
	// everything is placed.
	for _, s := range k.spare {
		if p := k.placed[s.pk]; p.stamp != k.stamp {
			delete(k.placed, s.pk)
		}
		if s.p.stamp != k.stamp {
			dst = graphics.AppendKittyDeletePlacement(dst, s.p.id, s.p.pid)
		}
	}
	for key, e := range k.ids {
		if e.stamp != k.stamp {
			dst = graphics.AppendKittyFree(dst, e.id)
			delete(k.ids, key)
		}
	}
	return dst
}

// appendPlace appends placement p on the cells pk names.
func (k *kittyImages) appendPlace(dst []byte, pk placeKey, p kittyPlaced) []byte {
	dst = buffer.AppendCursor(dst, pk.area.X, pk.area.Y)
	return graphics.AppendKittyPlaceID(dst, p.id, p.pid, pk.area.Width, pk.area.Height, p.x, p.y, p.w, p.h, p.z)
}

// placeKey names the next placement on area in the frame being placed.
func (k *kittyImages) placeKey(area cell.Rect) placeKey {
	n := k.seen[area]
	k.seen[area] = n + 1
	return placeKey{area, n}
}

// takeSpare returns a spare placement of picture id, and marks it taken.
func (k *kittyImages) takeSpare(id uint32) (kittyPlaced, bool) {
	i, ok := k.spareAt[id]
	if !ok {
		return kittyPlaced{}, false
	}
	if next := k.spare[i].next; next >= 0 {
		k.spareAt[id] = next
	} else {
		delete(k.spareAt, id)
	}
	k.spare[i].p.stamp = k.stamp
	return k.spare[i].p, true
}

// kittyPicture returns the picture a region shows as kitty is sent it, and
// the source rectangle x, y, w, h to show of it (all 0 for all of it).
func kittyPicture(reg ImageRegion, cellW, cellH uint16) (key kittyKey, x, y, w, h int) {
	full, cols, rows := reg.Img, reg.Area.Width, reg.Area.Height
	if c, ok := reg.Img.(*graphics.Clip); ok && c.Rows > 0 && c.Bottom > c.Top {
		full, cols, rows = c.Full, uint16(c.Cols), uint16(c.Rows)
		// The source rectangle is in the pixels of the picture sent,
		// which KittyCanvas may have made smaller than the cells.
		fw, fh := graphics.KittyCanvas(full, cols, rows, cellW, cellH)
		w, y, h = fw, c.Top*fh/c.Rows, (c.Bottom-c.Top)*fh/c.Rows
	}
	return kittyKey{full, cols, rows, cellW, cellH, reg.Transparent}, x, y, w, h
}

// freeAll appends the deletion of every picture and forgets them.
func (k *kittyImages) freeAll(dst []byte) []byte {
	clear(k.ids)
	clear(k.placed)
	return append(dst, graphics.KittyFreeAll...)
}
