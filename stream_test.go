package voxvault

import (
	"testing"
)

// TestStreaming200MB feeds a generated valid stream of over 200 MiB page by
// page, verifying continuous output and bounded internal buffers.
func TestStreaming200MB(t *testing.T) {
	const target = 200 << 20
	const serial = 1234
	const pktSamples = 4 * 960 // 4 frames of 20 ms

	var (
		count        int
		prevEnd      int64
		totalSamples int64
	)
	d := New(func(p Packet) {
		if p.Start != prevEnd {
			t.Errorf("packet %d starts at %d, want %d", count, p.Start, prevEnd)
		}
		prevEnd = p.End
		totalSamples += int64(p.Samples - p.HeadTrim - p.TailTrim)
		count++
	})

	seq := uint32(0)
	next := func() uint32 { s := seq; seq++; return s }
	feed := func(page []byte) {
		if err := d.Feed(page); err != nil {
			t.Fatalf("feed: %v", err)
		}
		if len(d.rd.buf) > 27+255+255*255 {
			t.Fatalf("page reader buffer grew to %d", len(d.rd.buf))
		}
		if len(d.cur) > maxPacketBytes {
			t.Fatalf("partial packet buffer grew to %d", len(d.cur))
		}
	}

	fed := 0
	feedPage := func(page []byte) {
		feed(page)
		fed += len(page)
	}

	feedPage(buildPage(serial, next(), 0x02, 0, laceChunks(makeHeadPacket(0, 2))))
	feedPage(buildPage(serial, next(), 0x00, 0, laceChunks(makeTagsPacket())))

	var cum int64
	var chunks [][]byte
	var pageDone, pageCont, midPacket bool
	flush := func(eos bool) {
		g := uint64(cum)
		if !pageDone && !eos {
			g = ^uint64(0) // -1
		}
		htype := byte(0)
		if pageCont {
			htype |= 0x01
		}
		if eos {
			htype |= 0x04
		}
		feedPage(buildPage(serial, next(), htype, g, chunks))
		chunks = nil
		pageDone = false
		pageCont = midPacket
	}

	for i := 0; fed < target; i++ {
		pkt := fillPacket([]byte{toc(19, 3), 0x04}, 4*999, byte(i))
		for _, chunk := range laceChunks(pkt) {
			if len(chunks) == 250 {
				flush(false)
			}
			chunks = append(chunks, chunk)
			if len(chunk) < 255 {
				cum += pktSamples
				pageDone = true
				midPacket = false
			} else {
				midPacket = true
			}
		}
	}
	flush(true)

	if err := d.Finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if fed < target {
		t.Fatalf("fed only %d bytes", fed)
	}
	if count == 0 {
		t.Fatal("no packets emitted")
	}
	if totalSamples != cum {
		t.Fatalf("emitted %d samples, want cumulative %d", totalSamples, cum)
	}
	if prevEnd != cum {
		t.Fatalf("output timeline ends at %d, want %d", prevEnd, cum)
	}
	t.Logf("fed %d bytes, %d packets, %d samples", fed, count, totalSamples)
}
