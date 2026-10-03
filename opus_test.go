package voxvault

import "testing"

func TestPacketDuration(t *testing.T) {
	cases := []struct {
		name string
		pkt  []byte
		want int
	}{
		{"code0 silk 10ms", pktCode0(0, 50), 480},
		{"code0 silk 60ms", pktCode0(3, 50), 2880},
		{"code0 hybrid 20ms", pktCode0(13, 50), 960},
		{"code0 celt 2.5ms", pktCode0(16, 50), 120},
		{"code0 celt 5ms", pktCode0(17, 50), 240},
		{"code0 celt 20ms fb", pktCode0(31, 50), 960},
		{"code1 two 20ms", pktCode1(19, 100), 1920},
		{"code1 two 60ms max", pktCode1(3, 100), 5760},
		{"code2 unequal", pktCode2(18, 100, 150), 960},
		{"code3 cbr 4x2.5ms", pktCode3CBR(16, 4, 25, 0), 480},
		{"code3 cbr padded", pktCode3CBR(17, 3, 40, 7), 720},
		{"code3 cbr long padlen", pktCode3CBR(16, 2, 10, 300), 240},
		{"code3 vbr", pktCode3VBR(19, []int{10, 20, 30}, 0), 2880},
		{"code3 vbr 2byte size", pktCode3VBR(16, []int{300, 10}, 0), 240},
		{"code3 vbr padded", pktCode3VBR(16, []int{10, 20, 30}, 5), 360},
		{"code3 48 frames max", pktCode3CBR(16, 48, 1, 0), 5760},
		{"code0 max frame", pktCode0(19, 1275), 960},
	}
	for _, c := range cases {
		got, err := packetDuration(c.pkt)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %d samples, want %d", c.name, got, c.want)
		}
	}
}

func TestPacketDurationErrors(t *testing.T) {
	cases := []struct {
		name string
		pkt  []byte
	}{
		{"empty", []byte{}},
		{"code1 odd payload", []byte{toc(19, 1), 1, 2, 3}},
		{"code2 truncated size", []byte{toc(19, 2)}},
		{"code2 truncated 2byte size", []byte{toc(19, 2), 255}},
		{"code2 negative second", pktCode2Raw(19, 100, 50)},
		{"code3 count zero", []byte{toc(16, 3), 0, 1, 2}},
		{"code3 over 120ms", pktCode3CBR(16, 49, 1, 0)},
		{"code3 cbr not divisible", pktCode3CBR(16, 3, 10, 2)[:13]},
		{"code3 padding overflow", []byte{toc(16, 3), 0x40 | 2, 200, 1}},
		{"code3 truncated padlen", []byte{toc(16, 3), 0x40 | 2}},
		{"code3 last frame too large", pktCode3VBR(16, []int{1, 1276}, 0)},
		{"code0 frame too large", pktCode0(19, 1276)},
		{"code1 frame too large", pktCode1(19, 1276)},
	}
	for _, c := range cases {
		if got, err := packetDuration(c.pkt); err == nil {
			t.Errorf("%s: want error, got %d samples", c.name, got)
		}
	}
}

// pktCode2Raw builds a code-2 packet whose declared first frame leaves
// a negative second frame.
func pktCode2Raw(config, f1, body int) []byte {
	b := make([]byte, 2+body)
	b[0] = toc(config, 2)
	b[1] = byte(f1)
	return b
}

func TestParseOpusHead(t *testing.T) {
	good := make([]byte, 19)
	copy(good, "OpusHead")
	good[8] = 1
	good[9] = 2
	h, err := parseOpusHead(good)
	if err != nil {
		t.Fatalf("good head: %v", err)
	}
	if h.channels != 2 {
		t.Fatalf("channels=%d", h.channels)
	}

	mut := func(f func(b []byte)) []byte {
		b := append([]byte{}, good...)
		f(b)
		return b
	}
	bads := map[string][]byte{
		"short":    good[:18],
		"long":     append(append([]byte{}, good...), 0),
		"bad sig":  mut(func(b []byte) { b[0] = 'X' }),
		"version":  mut(func(b []byte) { b[8] = 2 }),
		"channels": mut(func(b []byte) { b[9] = 6 }),
		"mapping":  mut(func(b []byte) { b[18] = 1 }),
	}
	for name, b := range bads {
		if _, err := parseOpusHead(b); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}
