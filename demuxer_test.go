package voxvault

import (
	"bytes"
	"math/rand"
	"testing"
)

// fillPacket builds a packet with the given raw bytes following the TOC
// structure, filled with deterministic content.
func fillPacket(prefix []byte, bodyLen int, seed byte) []byte {
	pkt := make([]byte, len(prefix)+bodyLen)
	copy(pkt, prefix)
	for i := len(prefix); i < len(pkt); i++ {
		pkt[i] = byte(i*31) + seed
	}
	return pkt
}

// toc returns a TOC byte for the given config and frame count code.
func toc(config, code int) byte { return byte(config<<3 | code) }

func mixedPackets() (packets [][]byte, samples []int) {
	packets = [][]byte{
		fillPacket([]byte{toc(18, 0)}, 300, 1),              // 480
		fillPacket([]byte{toc(16, 0)}, 254, 2),              // 120, total 255 bytes
		fillPacket([]byte{toc(19, 0)}, 509, 3),              // 960, total 510 bytes
		fillPacket([]byte{toc(19, 1)}, 200, 4),              // 2x960
		fillPacket([]byte{toc(18, 2), 50}, 130, 5),          // 960 (50+80)
		fillPacket([]byte{toc(16, 3), 0x03}, 120, 6),        // 3x120 CBR
		fillPacket([]byte{toc(17, 3), 0x83, 10, 20}, 60, 7), // 3x240 VBR
		fillPacket([]byte{toc(18, 3), 0xC2, 15, 7}, 32, 8),  // 2x480 VBR+pad7
		fillPacket([]byte{toc(19, 3), 0x04}, 1996, 9),       // 4x960 CBR, crosses pages
	}
	samples = []int{480, 120, 960, 1920, 960, 360, 720, 960, 3840}
	return packets, samples
}

func packetsEqual(a, b []Packet) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Samples != b[i].Samples || a[i].HeadTrim != b[i].HeadTrim ||
			a[i].TailTrim != b[i].TailTrim || a[i].Start != b[i].Start ||
			a[i].End != b[i].End || !bytes.Equal(a[i].Data, b[i].Data) {
			return false
		}
	}
	return true
}

func TestFeedSplitInvariance(t *testing.T) {
	packets, samples := mixedPackets()
	stream := concatPages(validStreamPages(streamCfg{
		preSkip:      312,
		packets:      packets,
		samples:      samples,
		segsPerPage:  5,
		finalGranule: -1,
	}))

	want, err := collect(stream)
	if err != nil {
		t.Fatalf("canonical run: %v", err)
	}
	if len(want) != len(packets) {
		t.Fatalf("got %d packets, want %d", len(want), len(packets))
	}
	// Hand-computed spot checks: pre-skip 312 eats 312 of packet 1 (480).
	if want[0].HeadTrim != 312 || want[0].Start != 0 || want[0].End != 168 {
		t.Fatalf("packet 0: %+v", want[0])
	}
	if want[1].Start != 168 || want[1].End != 288 {
		t.Fatalf("packet 1: %+v", want[1])
	}
	last := want[len(want)-1]
	if last.End != 10320-312 {
		t.Fatalf("last packet ends at %d, want %d", last.End, 10320-312)
	}
	for i, p := range want {
		if !bytes.Equal(p.Data, packets[i]) {
			t.Fatalf("packet %d data mismatch", i)
		}
		if p.Samples != samples[i] {
			t.Fatalf("packet %d samples %d, want %d", i, p.Samples, samples[i])
		}
		if i > 0 && p.Start != want[i-1].End {
			t.Fatalf("gap before packet %d: [%d,%d) after %d", i, p.Start, p.End, want[i-1].End)
		}
	}

	// Every single split position.
	for i := 0; i <= len(stream); i++ {
		got, err := collect(stream[:i], stream[i:])
		if err != nil {
			t.Fatalf("split at %d: %v", i, err)
		}
		if !packetsEqual(got, want) {
			t.Fatalf("split at %d: output differs", i)
		}
	}

	// Byte by byte.
	var got []Packet
	d := New(func(p Packet) { got = append(got, p) })
	for i := 0; i < len(stream); i++ {
		if err := d.Feed(stream[i : i+1]); err != nil {
			t.Fatalf("byte %d: %v", i, err)
		}
	}
	if err := d.Finish(); err != nil {
		t.Fatalf("byte-by-byte finish: %v", err)
	}
	if !packetsEqual(got, want) {
		t.Fatal("byte-by-byte: output differs")
	}

	// Random multi-chunk splits.
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 30; trial++ {
		got = nil
		d = New(func(p Packet) { got = append(got, p) })
		for pos := 0; pos < len(stream); {
			n := 1 + rng.Intn(97)
			if n > len(stream)-pos {
				n = len(stream) - pos
			}
			if err := d.Feed(stream[pos : pos+n]); err != nil {
				t.Fatalf("trial %d: %v", trial, err)
			}
			pos += n
		}
		if err := d.Finish(); err != nil {
			t.Fatalf("trial %d finish: %v", trial, err)
		}
		if !packetsEqual(got, want) {
			t.Fatalf("trial %d: output differs", trial)
		}
	}
}
