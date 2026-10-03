package voxvault

import (
	"testing"
)

// TestLargeStream pushes 200 MiB of legal stream through the demuxer
// page by page, verifying seamless output intervals and bounded
// internal buffers (no history retention).
func TestLargeStream(t *testing.T) {
	const targetBytes = 200 << 20

	var (
		out       int
		nextStart int64
		lastEnd   int64
	)
	d := New(func(p Packet) {
		if p.Start != nextStart {
			t.Errorf("packet %d starts at %d, want %d (gap/overlap)", out, p.Start, nextStart)
		}
		nextStart = p.End
		lastEnd = p.End
		out++
	})

	const preSkip = 312
	s := newOggStream(0xC0FFEE)
	s.addHeadPage(preSkip)
	s.addTagsPage()
	if err := d.Feed(s.buf); err != nil {
		t.Fatalf("headers: %v", err)
	}

	// Audio pages of 4 packets each: mixed durations, ~4 KiB per page.
	var cum uint64
	pkts := [][]byte{
		pktCode0(19, 700),                         // 960
		pktCode1(18, 300),                         // 960
		pktCode3CBR(16, 8, 100, 0),                // 8*120 = 960
		pktCode3VBR(17, []int{200, 300, 400}, 17), // 3*240 = 720
	}
	total := 0
	feed := func(b []byte) {
		for len(b) > 0 {
			n := 65536
			if n > len(b) {
				n = len(b)
			}
			if err := d.Feed(b[:n]); err != nil {
				t.Fatalf("feed at %d bytes: %v", total, err)
			}
			b = b[n:]
		}
	}
	for total < targetBytes {
		s.buf = s.buf[:0]
		for _, p := range pkts {
			samples, err := packetDuration(p)
			if err != nil {
				t.Fatalf("generator packet: %v", err)
			}
			cum += uint64(samples)
		}
		s.addPacketsPage(cum, 0, pkts...)
		feed(s.buf)
		total += len(s.buf)
		// Internal state must stay bounded: at most one staged page and
		// one in-flight packet.
		if len(d.buf) > pageMaxSize {
			t.Fatalf("page buffer grew to %d", len(d.buf))
		}
		if len(d.pkt) > maxPacketBytes {
			t.Fatalf("packet buffer grew to %d", len(d.pkt))
		}
	}
	// Final page: one more packet, EOS, granule = full total.
	s.buf = s.buf[:0]
	last := pktCode0(19, 10)
	cum += 960
	s.addPacketsPage(cum, flagEOS, last)
	feed(s.buf)
	if err := d.Finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if out == 0 {
		t.Fatalf("no packets emitted")
	}
	wantEnd := int64(cum) - preSkip
	if lastEnd != wantEnd {
		t.Fatalf("coverage ends at %d, want %d", lastEnd, wantEnd)
	}
	t.Logf("streamed %d bytes, %d packets, coverage [0,%d)", total+len(s.buf), out, lastEnd)
}
