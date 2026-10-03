package voxvault

import (
	"strings"
	"testing"
)

// buildThreePageAudioStream returns a stream with one audio packet on
// each of three audio pages, plus the cumulative granules.
func buildThreePageAudioStream() ([]byte, [][]byte) {
	p1 := pktCode0(19, 40) // 960
	p2 := pktCode0(19, 40) // 960
	p3 := pktCode0(19, 40) // 960
	s := newOggStream(0xBEEF)
	s.addHeadPage(0)
	s.addTagsPage()
	s.addPacketsPage(960, 0, p1)
	s.addPacketsPage(1920, 0, p2)
	s.addPacketsPage(2880, flagEOS, p3)
	return s.buf, [][]byte{p1, p2, p3}
}

func TestCRCCorruption(t *testing.T) {
	stream, pkts := buildThreePageAudioStream()
	// Find the start of the second audio page (head, tags, audio1
	// precede it) by scanning page boundaries with pageSize.
	off := 0
	var secondAudio int
	for i := 0; i < 3; i++ {
		n, err := pageSize(stream[off:])
		if err != nil {
			t.Fatalf("pageSize: %v", err)
		}
		off += n
	}
	secondAudio = off
	n, err := pageSize(stream[secondAudio:])
	if err != nil {
		t.Fatalf("pageSize: %v", err)
	}
	// Corrupt one payload byte inside the second audio page.
	bad := append([]byte{}, stream...)
	bad[secondAudio+n-1] ^= 0xFF

	var out []Packet
	d := New(func(p Packet) { out = append(out, p) })
	err = d.Feed(bad)
	if err == nil || !strings.Contains(err.Error(), "CRC") {
		t.Fatalf("want CRC error, got %v", err)
	}
	// The first audio page was fully validated before the error and its
	// output must not be rolled back; the corrupt page leaks nothing.
	if len(out) != 1 || string(out[0].Data) != string(pkts[0]) {
		t.Fatalf("outputs after CRC error: %d packets", len(out))
	}
	// The demuxer stays latched.
	if d.Feed(stream) == nil || d.Finish() == nil {
		t.Fatalf("demuxer did not latch the error")
	}
}

func TestContinuationFlagMismatch(t *testing.T) {
	// Page 2 ends mid-packet (last lacing 255) but page 3 lacks the
	// continuation flag.
	big := pktCode0(18, 600) // 601 bytes
	s := newOggStream(5)
	s.addHeadPage(0)
	s.addTagsPage()
	s.addRawPage(granuleNone, 0, []byte{255, 255}, big[:510])
	s.addRawPage(480, 0, []byte{91}, big[510:]) // missing flagContinued

	var out []Packet
	d := New(func(p Packet) { out = append(out, p) })
	err := d.Feed(s.buf)
	if err == nil || !strings.Contains(err.Error(), "continuation") {
		t.Fatalf("want continuation error, got %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("bad page leaked %d packets", len(out))
	}

	// Mirror image: page 2 ends on a packet boundary but page 3 claims
	// a continuation.
	p1 := pktCode0(19, 40)
	s2 := newOggStream(6)
	s2.addHeadPage(0)
	s2.addTagsPage()
	s2.addPacketsPage(960, 0, p1)
	s2.addPacketsPage(1920, flagContinued, p1)
	out = nil
	d = New(func(p Packet) { out = append(out, p) })
	err = d.Feed(s2.buf)
	if err == nil || !strings.Contains(err.Error(), "continuation") {
		t.Fatalf("want continuation error, got %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want exactly the first page's packet, got %d", len(out))
	}
}

func TestTruncation(t *testing.T) {
	stream, _ := buildThreePageAudioStream()

	// Cut inside the final page: Finish must report a truncated page.
	cut := len(stream) - 7
	d := New(func(Packet) {})
	if err := d.Feed(stream[:cut]); err != nil {
		t.Fatalf("feed of prefix failed: %v", err)
	}
	if err := d.Finish(); err == nil || !strings.Contains(err.Error(), "truncated page") {
		t.Fatalf("want truncated page error, got %v", err)
	}

	// Stream ending mid-packet: last page ends with lacing 255.
	big := pktCode0(18, 600)
	s := newOggStream(8)
	s.addHeadPage(0)
	s.addTagsPage()
	s.addRawPage(granuleNone, 0, []byte{255, 255}, big[:510])
	d = New(func(Packet) {})
	if err := d.Feed(s.buf); err != nil {
		t.Fatalf("feed failed: %v", err)
	}
	if err := d.Finish(); err == nil || !strings.Contains(err.Error(), "truncated packet") {
		t.Fatalf("want truncated packet error, got %v", err)
	}

	// No EOS page at all.
	s2 := newOggStream(9)
	s2.addHeadPage(0)
	s2.addTagsPage()
	s2.addPacketsPage(960, 0, pktCode0(19, 40))
	d = New(func(Packet) {})
	if err := d.Feed(s2.buf); err != nil {
		t.Fatalf("feed failed: %v", err)
	}
	if err := d.Finish(); err == nil || !strings.Contains(err.Error(), "EOS") {
		t.Fatalf("want missing EOS error, got %v", err)
	}
}

func TestGranuleViolations(t *testing.T) {
	// Non-final page completing a packet with a wrong granule.
	s := newOggStream(11)
	s.addHeadPage(0)
	s.addTagsPage()
	s.addPacketsPage(12345, 0, pktCode0(19, 40))
	d := New(func(Packet) {})
	if err := d.Feed(s.buf); err == nil || !strings.Contains(err.Error(), "granule") {
		t.Fatalf("want granule error, got %v", err)
	}

	// Page completing nothing must carry granule -1.
	s = newOggStream(12)
	s.addHeadPage(0)
	s.addTagsPage()
	big := pktCode0(18, 600)
	s.addRawPage(0, 0, []byte{255, 255}, big[:510]) // granule 0: illegal
	d = New(func(Packet) {})
	if err := d.Feed(s.buf); err == nil || !strings.Contains(err.Error(), "-1") {
		t.Fatalf("want granule -1 error, got %v", err)
	}

	// Final granule below the previously confirmed total.
	s = newOggStream(13)
	s.addHeadPage(0)
	s.addTagsPage()
	s.addPacketsPage(960, 0, pktCode0(19, 40))
	s.addPacketsPage(500, flagEOS, pktCode0(19, 40))
	d = New(func(Packet) {})
	if err := d.Feed(s.buf); err == nil || !strings.Contains(err.Error(), "final granule") {
		t.Fatalf("want final granule error, got %v", err)
	}

	// Final granule below pre-skip.
	s = newOggStream(14)
	s.addHeadPage(1000)
	s.addTagsPage()
	s.addPacketsPage(960, flagEOS, pktCode0(19, 40))
	d = New(func(Packet) {})
	if err := d.Feed(s.buf); err == nil || !strings.Contains(err.Error(), "pre-skip") {
		t.Fatalf("want pre-skip error, got %v", err)
	}
}

func TestSequenceAndSerial(t *testing.T) {
	stream, _ := buildThreePageAudioStream()
	// Drop the second audio page (seq 3): locate page boundaries.
	var bounds []int
	off := 0
	for off < len(stream) {
		bounds = append(bounds, off)
		n, err := pageSize(stream[off:])
		if err != nil {
			t.Fatalf("pageSize: %v", err)
		}
		off += n
	}
	bounds = append(bounds, len(stream))
	cut := append([]byte{}, stream[:bounds[3]]...)
	cut = append(cut, stream[bounds[4]:]...)
	d := New(func(Packet) {})
	if err := d.Feed(cut); err == nil || !strings.Contains(err.Error(), "sequence") {
		t.Fatalf("want sequence error, got %v", err)
	}

	// Serial change mid-stream.
	s := newOggStream(21)
	s.addHeadPage(0)
	s.addTagsPage()
	s.addPacketsPage(960, 0, pktCode0(19, 40))
	s.serial = 22 // same seq counter, different serial
	s.addPacketsPage(1920, flagEOS, pktCode0(19, 40))
	d = New(func(Packet) {})
	if err := d.Feed(s.buf); err == nil || !strings.Contains(err.Error(), "serial") {
		t.Fatalf("want serial error, got %v", err)
	}
}

func TestNoResyncOnGarbage(t *testing.T) {
	stream, _ := buildThreePageAudioStream()
	// Corrupt the capture pattern of an audio page; even though a valid
	// "OggS" follows later, the demuxer must fail instead of resyncing.
	var bounds []int
	off := 0
	for off < len(stream) {
		bounds = append(bounds, off)
		n, _ := pageSize(stream[off:])
		off += n
	}
	bad := append([]byte{}, stream...)
	bad[bounds[2]] = 'X'
	d := New(func(Packet) {})
	if err := d.Feed(bad); err == nil {
		t.Fatalf("want capture error")
	}
}
