package voxvault

import (
	"bytes"
	"testing"
)

// TestPreSkipAndFinalTrim exercises pre-skip spanning several packets, mixed
// frame durations including 2.5 ms frames, and a final page that drops one
// whole packet plus part of its neighbour. All expected values are
// hand-computed.
func TestPreSkipAndFinalTrim(t *testing.T) {
	// Durations: 120, 240, 480, 960, 240, 960, 480 (2.5/5/10/20 ms frames).
	packets := [][]byte{
		fillPacket([]byte{toc(16, 0)}, 10, 1),       // 120
		fillPacket([]byte{toc(17, 0)}, 10, 2),       // 240
		fillPacket([]byte{toc(18, 0)}, 10, 3),       // 480
		fillPacket([]byte{toc(19, 0)}, 10, 4),       // 960
		fillPacket([]byte{toc(16, 3), 0x02}, 20, 5), // 2x120
		fillPacket([]byte{toc(19, 0)}, 10, 6),       // 960
		fillPacket([]byte{toc(18, 0)}, 10, 7),       // 480
	}
	samples := []int{120, 240, 480, 960, 240, 960, 480}
	// Cumulative: 120, 360, 840, 1800, 2040, 3000, 3480.
	// pre-skip 500 drops p1, p2 and 140 samples of p3.
	// Final granule 2340 drops p7 entirely and 660 samples of p6.
	stream := concatPages(validStreamPages(streamCfg{
		preSkip:      500,
		packets:      packets,
		samples:      samples,
		segsPerPage:  255,
		finalGranule: 2340,
	}))

	got, err := collect(stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	want := []Packet{
		{Data: packets[2], Samples: 480, HeadTrim: 140, TailTrim: 0, Start: 0, End: 340},
		{Data: packets[3], Samples: 960, HeadTrim: 0, TailTrim: 0, Start: 340, End: 1300},
		{Data: packets[4], Samples: 240, HeadTrim: 0, TailTrim: 0, Start: 1300, End: 1540},
		{Data: packets[5], Samples: 960, HeadTrim: 0, TailTrim: 660, Start: 1540, End: 1840},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d packets, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if !packetsEqual(got[i:i+1], want[i:i+1]) {
			t.Fatalf("packet %d: got %+v want %+v", i, got[i], want[i])
		}
	}
	// The kept intervals tile [0, finalGranule-preSkip) without gaps.
	if got[0].Start != 0 || got[len(got)-1].End != 1840 {
		t.Fatalf("output span [%d,%d), want [0,1840)", got[0].Start, got[len(got)-1].End)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Start != got[i-1].End {
			t.Fatalf("gap between packets %d and %d", i-1, i)
		}
	}
}

// TestCrossPage255Boundary splits a 510-byte packet so the page boundary
// falls on a 255 multiple: the next page starts with a zero lacing value.
func TestCrossPage255Boundary(t *testing.T) {
	pkt := fillPacket([]byte{toc(19, 0)}, 509, 9) // 510 bytes, 960 samples
	pages := validStreamPages(streamCfg{
		packets:      [][]byte{pkt},
		samples:      []int{960},
		segsPerPage:  2,
		finalGranule: -1,
	})
	// Expect: head, tags, audio page [255,255] with granule -1, then a
	// continued EOS page with a single zero-length segment.
	if len(pages) != 4 {
		t.Fatalf("got %d pages, want 4", len(pages))
	}
	if pages[2][5] != 0 || pages[2][26] != 2 {
		t.Fatalf("page 2 flags/segments: %v", pages[2][27:])
	}
	if pages[3][5]&0x05 != 0x05 {
		t.Fatalf("page 3 should be continued+EOS, flags=%02x", pages[3][5])
	}
	if pages[3][27] != 0 {
		t.Fatalf("page 3 first lacing %d, want 0", pages[3][27])
	}
	got, err := collect(concatPages(pages))
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(got) != 1 || !bytes.Equal(got[0].Data, pkt) {
		t.Fatalf("got %d packets", len(got))
	}
	if got[0].Start != 0 || got[0].End != 960 || got[0].Samples != 960 {
		t.Fatalf("packet: %+v", got[0])
	}
}
