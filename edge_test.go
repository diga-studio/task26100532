package voxvault

import (
	"strings"
	"testing"
)

func TestPacketTooLarge(t *testing.T) {
	// A packet exceeding 128 KiB must be rejected even though the Ogg
	// lacing itself is fine.
	big := pktCode0(19, maxPacketBytes) // 1 + 131072 bytes > 128 KiB
	s := newOggStream(31)
	s.addHeadPage(0)
	s.addTagsPage()
	// Split into pages of 255*255 bytes (max page) plus remainder.
	rest := big
	first := true
	for len(rest) > 0 {
		n := 255 * 255
		if n > len(rest) {
			n = len(rest)
		}
		segs := make([]byte, n/255)
		for i := range segs {
			segs[i] = 255
		}
		var flags byte
		if !first {
			flags = flagContinued
		}
		first = false
		// Terminate lacing on the final chunk.
		if n == len(rest) {
			segs = lace(n)
		}
		s.addRawPage(granuleNone, flags, segs, rest[:n])
		rest = rest[n:]
	}
	d := New(func(Packet) {})
	if err := d.Feed(s.buf); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("want packet-too-large error, got %v", err)
	}
}

func TestLargeTagsPacket(t *testing.T) {
	// OpusTags larger than 255 bytes uses multi-segment lacing on its
	// single page.
	tags := make([]byte, 700)
	copy(tags, "OpusTags")
	p1 := pktCode0(19, 40)
	s := newOggStream(32)
	s.addHeadPage(0)
	s.addPacketsPage(0, 0, tags)
	s.addPacketsPage(960, flagEOS, p1)
	out, err := feedAll(t, s.buf)
	if err != nil {
		t.Fatalf("feed: %v", err)
	}
	if len(out) != 1 || out[0].End != 960 {
		t.Fatalf("got %d packets", len(out))
	}
}

func TestDataAfterEOS(t *testing.T) {
	s := newOggStream(33)
	s.addHeadPage(0)
	s.addTagsPage()
	s.addPacketsPage(960, flagEOS, pktCode0(19, 40))
	d := New(func(Packet) {})
	if err := d.Feed(s.buf); err != nil {
		t.Fatalf("feed: %v", err)
	}
	if err := d.Feed([]byte{0}); err == nil {
		t.Fatalf("want error for data after EOS")
	}
	// The failure is latched.
	if err := d.Finish(); err == nil {
		t.Fatalf("want latched error from Finish")
	}
}

func TestSequenceWrap(t *testing.T) {
	// Sequence numbers wrap as uint32.
	s := newOggStream(34)
	s.seq = 0xFFFFFFFE
	s.addHeadPage(0)
	s.addTagsPage()
	s.addPacketsPage(960, 0, pktCode0(19, 40))        // seq FFFFFFFF
	s.addPacketsPage(1920, flagEOS, pktCode0(19, 40)) // seq 0
	out, err := feedAll(t, s.buf)
	if err != nil {
		t.Fatalf("feed: %v", err)
	}
	if len(out) != 2 || out[1].End != 1920 {
		t.Fatalf("got %d packets", len(out))
	}
}

func TestEmptyAudioPage(t *testing.T) {
	// A zero-segment audio page completes nothing and must carry
	// granule -1.
	s := newOggStream(35)
	s.addHeadPage(0)
	s.addTagsPage()
	s.addRawPage(granuleNone, 0, nil, nil)
	s.addPacketsPage(960, flagEOS, pktCode0(19, 40))
	out, err := feedAll(t, s.buf)
	if err != nil {
		t.Fatalf("feed: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d packets", len(out))
	}
}
