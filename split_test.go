package voxvault

import (
	"math/rand"
	"reflect"
	"testing"
)

// buildSplitStream creates a legal stream exercising:
//   - a packet spanning two pages (C, split at a 255 boundary)
//   - 255-byte segments
//   - a packet whose length is an exact multiple of 255 (D, 510 bytes)
//   - code-0/2/3 packets and a padded VBR packet
//
// It returns the stream bytes and the packets in decode order.
func buildSplitStream() ([]byte, [][]byte) {
	preSkip := uint16(312)

	a := pktCode0(19, 100)                     // 960 samples
	b := pktCode3VBR(16, []int{10, 20, 30}, 5) // 3*120 = 360 samples
	c := pktCode0(18, 600)                     // 480 samples, 601 bytes: spans pages
	d := pktCode0(17, 509)                     // 240 samples, 510 bytes = 2*255
	e := pktCode2(19, 100, 150)                // 2*960 = 1920 samples

	s := newOggStream(0xDECAFBAD)
	s.addHeadPage(preSkip)
	s.addTagsPage()

	// Page 2: A and B complete; granule = 960+360 = 1320.
	s.addPacketsPage(1320, 0, a, b)

	// Page 3: first 510 bytes of C (two 255 segments), completes nothing.
	s.addRawPage(granuleNone, 0, []byte{255, 255}, c[:510])

	// Page 4: rest of C (91 bytes), then D (255,255,0 lacing).
	segs := append([]byte{91}, lace(len(d))...)
	body := append(append([]byte{}, c[510:]...), d...)
	s.addRawPage(1320+480+240, flagContinued, segs, body)

	// Page 5 (EOS): E; granule = full total.
	total := uint64(960 + 360 + 480 + 240 + 1920)
	s.addPacketsPage(total, flagEOS, e)

	return s.buf, [][]byte{a, b, c, d, e}
}

func packetsEqual(a, b []Packet) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !reflect.DeepEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func TestSplitInvariance(t *testing.T) {
	stream, pkts := buildSplitStream()

	ref, err := feedAll(t, stream)
	if err != nil {
		t.Fatalf("reference run failed: %v", err)
	}
	if len(ref) != len(pkts) {
		t.Fatalf("reference emitted %d packets, want %d", len(ref), len(pkts))
	}
	for i, p := range ref {
		if !reflect.DeepEqual(p.Data, pkts[i]) {
			t.Fatalf("packet %d bytes not faithful", i)
		}
	}
	// Hand-checked timeline: preSkip 312 trims A to [0,648).
	wantIntervals := [][2]int64{{0, 648}, {648, 1008}, {1008, 1488}, {1488, 1728}, {1728, 3648}}
	wantTrim := [][2]int{{312, 0}, {0, 0}, {0, 0}, {0, 0}, {0, 0}}
	for i, p := range ref {
		if p.Start != wantIntervals[i][0] || p.End != wantIntervals[i][1] {
			t.Fatalf("packet %d interval [%d,%d), want %v", i, p.Start, p.End, wantIntervals[i])
		}
		if p.TrimStart != wantTrim[i][0] || p.TrimEnd != wantTrim[i][1] {
			t.Fatalf("packet %d trims (%d,%d), want %v", i, p.TrimStart, p.TrimEnd, wantTrim[i])
		}
	}

	check := func(name string, sizes []int) {
		t.Helper()
		got, err := feedChunks(stream, sizes)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !packetsEqual(got, ref) {
			t.Fatalf("%s: outputs differ from reference", name)
		}
	}

	// Every single split position.
	for i := 0; i <= len(stream); i++ {
		check("split@"+itoa(i), []int{i})
	}
	// Byte by byte.
	ones := make([]int, len(stream))
	for i := range ones {
		ones[i] = 1
	}
	check("bytewise", ones)
	// Random multi-splits.
	r := rand.New(rand.NewSource(42))
	for j, plan := range randomSplits(r, len(stream), 60) {
		check("random#"+itoa(j), plan)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
