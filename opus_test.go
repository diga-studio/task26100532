package voxvault

import "testing"

func TestFrameDurationTable(t *testing.T) {
	cases := map[int]int{
		0: 480, 1: 960, 2: 1920, 3: 2880, // SILK 10/20/40/60 ms
		4: 480, 5: 960, 6: 1920, 7: 2880, // SILK
		8: 480, 9: 960, 10: 1920, 11: 2880, // SILK
		12: 480, 13: 960, 14: 480, 15: 960, // Hybrid 10/20 ms
		16: 120, 17: 240, 18: 480, 19: 960, // CELT 2.5/5/10/20 ms
		28: 120, 31: 960,
	}
	for config, want := range cases {
		if got := frameDurationSamples(config); got != want {
			t.Fatalf("config %d: %d samples, want %d", config, got, want)
		}
	}
}

func TestOpusPacketSamples(t *testing.T) {
	valid := []struct {
		name string
		pkt  []byte
		want int
	}{
		{"code0 2.5ms", fillPacket([]byte{toc(16, 0)}, 10, 1), 120},
		{"code0 20ms", fillPacket([]byte{toc(19, 0)}, 100, 1), 960},
		{"code0 60ms silk", fillPacket([]byte{toc(3, 0)}, 100, 1), 2880},
		{"code1", fillPacket([]byte{toc(19, 1)}, 200, 1), 1920},
		{"code1 empty frames", []byte{toc(18, 0+1)}, 960},
		{"code2", fillPacket([]byte{toc(18, 2), 50}, 130, 1), 960},
		{"code2 empty second", fillPacket([]byte{toc(18, 2), 60}, 60, 1), 960},
		{"code3 cbr", fillPacket([]byte{toc(16, 3), 0x03}, 120, 1), 360},
		{"code3 vbr", fillPacket([]byte{toc(17, 3), 0x83, 10, 20}, 60, 1), 720},
		{"code3 vbr wide size", fillPacket([]byte{toc(18, 3), 0x82, 252, 12}, 400, 1), 960},
		{"code3 vbr padding", fillPacket([]byte{toc(18, 3), 0xC2, 15, 7}, 32, 1), 960},
		{"code3 cbr padding", fillPacket([]byte{toc(19, 3), 0x42, 9}, 91, 1), 1920},
		{"code3 max frames 2.5ms", fillPacket([]byte{toc(16, 3), 0x30}, 48*25, 1), 5760},
		{"code3 120ms silk", fillPacket([]byte{toc(3, 3), 0x02}, 200, 1), 5760},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			got, err := opusPacketSamples(tc.pkt)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %d samples, want %d", got, tc.want)
			}
		})
	}

	invalid := []struct {
		name string
		pkt  []byte
	}{
		{"empty", nil},
		{"code1 odd body", fillPacket([]byte{toc(18, 1)}, 99, 1)},
		{"code2 first frame too big", fillPacket([]byte{toc(18, 2), 200}, 100, 1)},
		{"code3 zero frames", fillPacket([]byte{toc(18, 3), 0x00}, 100, 1)},
		{"code3 cbr not divisible", fillPacket([]byte{toc(18, 3), 0x03}, 100, 1)},
		{"code3 vbr sizes exceed", fillPacket([]byte{toc(18, 3), 0x82, 200}, 100, 1)},
		{"code3 vbr truncated sizes", []byte{toc(18, 3), 0x83, 10}},
		{"code3 padding exceeds", fillPacket([]byte{toc(18, 3), 0x42, 250}, 50, 1)},
		{"code3 truncated padding", []byte{toc(18, 3), 0x42}},
		{"over 120ms", fillPacket([]byte{toc(19, 3), 0x07}, 700, 1)},
		{"49 frames of 2.5ms", fillPacket([]byte{toc(16, 3), 0x31}, 49*25, 1)},
		{"frame too large", fillPacket([]byte{toc(18, 0)}, 1276, 1)},
		{"cbr frame too large", fillPacket([]byte{toc(18, 3), 0x02}, 2552, 1)},
	}
	for _, tc := range invalid {
		t.Run("bad/"+tc.name, func(t *testing.T) {
			if got, err := opusPacketSamples(tc.pkt); err == nil {
				t.Fatalf("want error, got %d samples", got)
			}
		})
	}
}

func TestParseOpusHead(t *testing.T) {
	head, err := parseOpusHead(makeHeadPacket(312, 2))
	if err != nil {
		t.Fatalf("valid head: %v", err)
	}
	if head.preSkip != 312 || head.channels != 2 {
		t.Fatalf("got %+v", head)
	}

	// The input sample rate field must not influence anything.
	pkt := makeHeadPacket(100, 1)
	pkt[12], pkt[13], pkt[14], pkt[15] = 0x44, 0xAC, 0, 0 // 44100
	head, err = parseOpusHead(pkt)
	if err != nil || head.preSkip != 100 {
		t.Fatalf("input rate must be ignored: %+v, %v", head, err)
	}

	bad := []struct {
		name   string
		mutate func([]byte)
	}{
		{"signature", func(p []byte) { p[0] = 'X' }},
		{"version", func(p []byte) { p[8] = 2 }},
		{"channels zero", func(p []byte) { p[9] = 0 }},
		{"channels three", func(p []byte) { p[9] = 3 }},
		{"mapping family", func(p []byte) { p[18] = 1 }},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			p := makeHeadPacket(0, 2)
			tc.mutate(p)
			if _, err := parseOpusHead(p); err == nil {
				t.Fatal("want error")
			}
		})
	}
	if _, err := parseOpusHead(makeHeadPacket(0, 2)[:18]); err == nil {
		t.Fatal("short head: want error")
	}
}
