package voxvault

import "testing"

// Hand-computed timeline: pre-skip spans multiple packets, frame
// durations are mixed (including 2.5 ms frames), and the final page
// granule trims one whole packet plus part of its predecessor.
//
// Packets (decode timeline, samples at 48 kHz):
//
//	P1 code0 cfg16  1x120  = 120   [0,120)
//	P2 code3 cfg16  4x120  = 480   [120,600)
//	P3 code1 cfg17  2x240  = 480   [600,1080)
//	P4 code0 cfg19  1x960  = 960   [1080,2040)
//	P5 code0 cfg16  1x120  = 120   [2040,2160)
//
// preSkip = 500, final granule = 1620 (trims all of P5 and 420 of P4).
func buildTimelineStream() ([]byte, [][]byte) {
	const preSkip = 500
	p1 := pktCode0(16, 40)
	p2 := pktCode3CBR(16, 4, 25, 0)
	p3 := pktCode1(17, 60)
	p4 := pktCode0(19, 200)
	p5 := pktCode0(16, 33)

	s := newOggStream(7)
	s.addHeadPage(preSkip)
	s.addTagsPage()
	s.addPacketsPage(600, 0, p1, p2) // granule = 120+480
	s.addPacketsPage(1080, 0, p3)    // granule = 1080
	s.addPacketsPage(1620, flagEOS, p4, p5)
	return s.buf, [][]byte{p1, p2, p3, p4, p5}
}

func TestTimelineHandComputed(t *testing.T) {
	stream, pkts := buildTimelineStream()
	out, err := feedAll(t, stream)
	if err != nil {
		t.Fatalf("feed: %v", err)
	}

	// P1 fully consumed by pre-skip, P5 fully trimmed by final granule.
	if len(out) != 3 {
		t.Fatalf("emitted %d packets, want 3", len(out))
	}

	type want struct {
		pkt        int // index into pkts
		samples    int
		trimStart  int
		trimEnd    int
		start, end int64
	}
	wants := []want{
		{pkt: 1, samples: 480, trimStart: 380, trimEnd: 0, start: 0, end: 100},
		{pkt: 2, samples: 480, trimStart: 0, trimEnd: 0, start: 100, end: 580},
		{pkt: 3, samples: 960, trimStart: 0, trimEnd: 420, start: 580, end: 1120},
	}
	for i, w := range wants {
		p := out[i]
		if p.Samples != w.samples || p.TrimStart != w.trimStart || p.TrimEnd != w.trimEnd {
			t.Errorf("packet %d: samples=%d trims=(%d,%d), want %d (%d,%d)",
				i, p.Samples, p.TrimStart, p.TrimEnd, w.samples, w.trimStart, w.trimEnd)
		}
		if p.Start != w.start || p.End != w.end {
			t.Errorf("packet %d: interval [%d,%d), want [%d,%d)", i, p.Start, p.End, w.start, w.end)
		}
		if string(p.Data) != string(pkts[w.pkt]) {
			t.Errorf("packet %d: bytes differ from source packet %d", i, w.pkt)
		}
	}

	// Kept intervals tile [0, finalGranule-preSkip) = [0,1120) seamlessly.
	var pos int64
	for _, p := range out {
		if p.Start != pos {
			t.Fatalf("gap/overlap at %d, want %d", p.Start, pos)
		}
		pos = p.End
	}
	if pos != 1120 {
		t.Fatalf("coverage ends at %d, want 1120", pos)
	}
}

// The final page may trim every sample of several trailing packets.
func TestFinalPageTrimsMultipleWholePackets(t *testing.T) {
	const preSkip = 0
	p1 := pktCode0(19, 50) // 960
	p2 := pktCode0(19, 50) // 960
	p3 := pktCode0(19, 50) // 960
	p4 := pktCode0(19, 50) // 960

	s := newOggStream(9)
	s.addHeadPage(preSkip)
	s.addTagsPage()
	s.addPacketsPage(960, 0, p1)
	// Final page completes p2..p4 (cum 3840) but granule keeps only p2
	// and 100 samples of p3.
	s.addPacketsPage(1920+100, flagEOS, p2, p3, p4)

	out, err := feedAll(t, s.buf)
	if err != nil {
		t.Fatalf("feed: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("emitted %d packets, want 3", len(out))
	}
	if out[0].Start != 0 || out[0].End != 960 {
		t.Errorf("p1 interval [%d,%d), want [0,960)", out[0].Start, out[0].End)
	}
	if out[1].TrimEnd != 0 || out[1].Start != 960 || out[1].End != 1920 {
		t.Errorf("p2 trim=%d interval [%d,%d), want trim 0 [960,1920)",
			out[1].TrimEnd, out[1].Start, out[1].End)
	}
	if out[2].TrimEnd != 860 || out[2].Start != 1920 || out[2].End != 2020 {
		t.Errorf("p3 trim=%d interval [%d,%d), want trim 860 [1920,2020)",
			out[2].TrimEnd, out[2].Start, out[2].End)
	}
}
