package nat

import (
	"encoding/binary"
	"net"
	"testing"
)

func buildResponse(tx TransactionID, ip net.IP, port int, xor bool) []byte {
	var val []byte
	family := byte(0x01)
	raw := ip.To4()
	if raw == nil {
		family = 0x02
		raw = ip.To16()
	}
	val = append(val, 0, family, 0, 0)
	p := uint16(port)
	addr := append(net.IP(nil), raw...)
	attrType := uint16(attrMappedAddress)
	if xor {
		attrType = attrXorMappedAddr
		p ^= uint16(stunMagicCookie >> 16)
		var key [16]byte
		binary.BigEndian.PutUint32(key[0:4], stunMagicCookie)
		copy(key[4:], tx[:])
		for i := range addr {
			addr[i] ^= key[i]
		}
	}
	binary.BigEndian.PutUint16(val[2:4], p)
	val = append(val, addr...)

	msg := make([]byte, stunHeaderSize)
	binary.BigEndian.PutUint16(msg[0:2], stunBindingSuccess)
	binary.BigEndian.PutUint32(msg[4:8], stunMagicCookie)
	copy(msg[8:20], tx[:])
	attr := make([]byte, 4)
	binary.BigEndian.PutUint16(attr[0:2], attrType)
	binary.BigEndian.PutUint16(attr[2:4], uint16(len(val)))
	attr = append(attr, val...)
	for len(attr)%4 != 0 {
		attr = append(attr, 0)
	}
	msg = append(msg, attr...)
	binary.BigEndian.PutUint16(msg[2:4], uint16(len(msg)-stunHeaderSize))
	return msg
}

func TestParseBindingResponse(t *testing.T) {
	_, tx := NewBindingRequest()
	for _, tc := range []struct {
		ip  string
		xor bool
	}{{"24.133.15.62", true}, {"24.133.15.62", false}, {"2001:db8::42", true}} {
		resp := buildResponse(tx, net.ParseIP(tc.ip), 40010, tc.xor)
		gotTx, addr, err := ParseBindingResponse(resp)
		if err != nil || gotTx != tx || !addr.IP.Equal(net.ParseIP(tc.ip)) || addr.Port != 40010 {
			t.Fatalf("%s xor=%v: got %v %v", tc.ip, tc.xor, addr, err)
		}
	}
	if _, _, err := ParseBindingResponse([]byte("garbage-garbage-garbage")); err == nil {
		t.Fatal("garbage parsed")
	}
}

func TestClassifier(t *testing.T) {
	c := NewClassifier()
	_, tx1 := NewBindingRequest()
	_, tx2 := NewBindingRequest()
	c.Track(tx1, "1.1.1.1:3478")
	c.Track(tx2, "8.8.8.8:19302")
	pub := net.ParseIP("135.136.39.6")
	c.Observe(tx1, &net.UDPAddr{IP: pub, Port: 40010})
	if typ, _ := c.Result(); typ != TypeUnknown {
		t.Fatalf("classified with one observation: %v", typ)
	}
	if !c.Observe(tx2, &net.UDPAddr{IP: pub, Port: 16441}) {
		t.Fatal("classification change not reported")
	}
	if typ, _ := c.Result(); typ != TypeEDM {
		t.Fatalf("expected EDM, got %v", typ)
	}

	c.StartRound()
	c.Track(tx1, "1.1.1.1:3478")
	c.Track(tx2, "8.8.8.8:19302")
	c.Observe(tx1, &net.UDPAddr{IP: pub, Port: 50001})
	c.Observe(tx2, &net.UDPAddr{IP: pub, Port: 50001})
	if typ, p := c.Result(); typ != TypeEIM || p.Port != 50001 {
		t.Fatalf("expected EIM :50001, got %v %v", typ, p)
	}
	// Unknown transaction IDs are ignored.
	_, stray := NewBindingRequest()
	if c.Observe(stray, &net.UDPAddr{IP: pub, Port: 1}) {
		t.Fatal("stray response accepted")
	}
}

func TestStrategyAndCandidates(t *testing.T) {
	if ChooseStrategy(TypeEDM, TypeEDM) != StrategyRelayOnly || ChooseStrategy(TypeEIM, TypeEDM) != StrategySprayRemote ||
		ChooseStrategy(TypeEDM, TypeEIM) != StrategyManySockets || ChooseStrategy(TypeUnknown, TypeEDM) != StrategyDirect {
		t.Fatal("unexpected strategy")
	}
	cands := BuildCandidates("24.133.15.62", 40000, "192.168.1.5", 50000, []string{"[2001:db8::1]:50000", "[fd00::1]:50000", "bogus"}, false)
	if len(cands) != 2 || cands[0].Kind != "v4" || cands[1].Kind != "v6" {
		t.Fatalf("unexpected candidates: %+v", cands)
	}
	lan := BuildCandidates("192.168.1.5", 0, "192.168.1.5", 50000, nil, true)
	if len(lan) != 1 || lan[0].Addr.Port != 50000 {
		t.Fatalf("unexpected LAN candidates: %+v", lan)
	}
	ports := RandomPorts(300, 5000)
	seen := map[int]bool{}
	for _, p := range ports {
		if p < 1024 || p == 5000 || seen[p] {
			t.Fatalf("bad port %d", p)
		}
		seen[p] = true
	}
}
