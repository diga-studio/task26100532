package voxvault

import (
	"math"
	"strings"
	"testing"
)

// sevenPacketPages returns a valid stream of 7 single-segment packets split
// across two audio pages (4 + 3, the second being EOS).
func sevenPacketPages() (pages [][]byte, packets [][]byte) {
	for i := 0; i < 7; i++ {
		packets = append(packets, fillPacket([]byte{toc(18, 0)}, 100, byte(i)))
	}
	samples := []int{480, 480, 480, 480, 480, 480, 480}
	pages = validStreamPages(streamCfg{
		packets:      packets,
		samples:      samples,
		segsPerPage:  4,
		finalGranule: -1,
	})
	return pages, packets
}

func TestBadCRC(t *testing.T) {
	pages, _ := sevenPacketPages()

	// Corrupt a payload byte of the final (EOS) page.
	bad := append([]byte(nil), pages[len(pages)-1]...)
	bad[len(bad)-1] ^= 0xFF
	stream := concatPages(append(append([][]byte{}, pages[:len(pages)-1]...), bad))
	got, err := collect(stream)
	if err == nil || !strings.Contains(err.Error(), "CRC") {
		t.Fatalf("want CRC error, got %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("corrupt final page leaked output: got %d packets, want 4", len(got))
	}

	// Corrupt the first audio page: no audio packet may be emitted.
	bad = append([]byte(nil), pages[2]...)
	bad[len(bad)-1] ^= 0xFF
	stream = concatPages([][]byte{pages[0], pages[1], bad})
	got, err = collect(stream)
	if err == nil || !strings.Contains(err.Error(), "CRC") {
		t.Fatalf("want CRC error, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("corrupt page leaked %d packets", len(got))
	}
}

func TestContinuationFlagMismatch(t *testing.T) {
	pkt := fillPacket([]byte{toc(19, 0)}, 509, 9) // 510 bytes
	pages := validStreamPages(streamCfg{
		packets:      [][]byte{pkt},
		samples:      []int{960},
		segsPerPage:  2,
		finalGranule: -1,
	})

	// The packet spans pages 3 and 4; clear the continuation flag on page 4.
	bad := append([]byte(nil), pages[3]...)
	bad[5] &^= 0x01
	fixCRC(bad)
	stream := concatPages([][]byte{pages[0], pages[1], pages[2], bad})
	got, err := collect(stream)
	if err == nil || !strings.Contains(err.Error(), "continuation") {
		t.Fatalf("want continuation error, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("bad page leaked %d packets", len(got))
	}

	// Set the continuation flag on a page with no pending packet.
	bad = append([]byte(nil), pages[2]...)
	bad[5] |= 0x01
	fixCRC(bad)
	stream = concatPages([][]byte{pages[0], pages[1], bad})
	got, err = collect(stream)
	if err == nil || !strings.Contains(err.Error(), "continuation") {
		t.Fatalf("want continuation error, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("bad page leaked %d packets", len(got))
	}
}

func TestTruncation(t *testing.T) {
	pages, _ := sevenPacketPages()
	stream := concatPages(pages)

	// Truncated final page: Finish must complain.
	got, err := collect(stream[:len(stream)-10])
	if err == nil || !strings.Contains(err.Error(), "truncated page") {
		t.Fatalf("want truncated page error, got %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d packets before truncation, want 4", len(got))
	}

	// Stream ends mid-packet with no EOS page.
	pkt := fillPacket([]byte{toc(19, 0)}, 509, 9)
	mid := validStreamPages(streamCfg{
		packets:      [][]byte{pkt},
		samples:      []int{960},
		segsPerPage:  2,
		finalGranule: -1,
	})
	_, err = collect(concatPages(mid[:3])) // head, tags, [255,255] page
	if err == nil || !strings.Contains(err.Error(), "truncated packet") {
		t.Fatalf("want truncated packet error, got %v", err)
	}

	// Missing EOS flag on the final page.
	last := append([]byte(nil), pages[len(pages)-1]...)
	last[5] &^= 0x04
	fixCRC(last)
	stream = concatPages(append(append([][]byte{}, pages[:len(pages)-1]...), last))
	_, err = collect(stream)
	if err == nil || !strings.Contains(err.Error(), "end-of-stream") {
		t.Fatalf("want missing EOS error, got %v", err)
	}
}

func TestSequenceWrapAndGap(t *testing.T) {
	packets, samples := mixedPackets()
	// Sequence numbers wrap past 2^32.
	pages := validStreamPages(streamCfg{
		startSeq:     0xFFFFFFFE,
		packets:      packets,
		samples:      samples,
		segsPerPage:  5,
		finalGranule: -1,
	})
	if _, err := collect(concatPages(pages)); err != nil {
		t.Fatalf("wrap stream: %v", err)
	}

	// Dropping a page breaks continuity.
	if len(pages) < 4 {
		t.Fatalf("need at least 4 pages, got %d", len(pages))
	}
	broken := append(append([][]byte{}, pages[:3]...), pages[4:]...)
	_, err := collect(concatPages(broken))
	if err == nil || !strings.Contains(err.Error(), "sequence") {
		t.Fatalf("want sequence error, got %v", err)
	}
}

func TestOversizePacket(t *testing.T) {
	big := fillPacket([]byte{toc(19, 3), 0x04}, maxPacketBytes, 1) // 128 KiB + 2 bytes
	pages := validStreamPages(streamCfg{
		packets:      [][]byte{big},
		samples:      []int{3840},
		segsPerPage:  255,
		finalGranule: -1,
	})
	_, err := collect(concatPages(pages))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("want oversize packet error, got %v", err)
	}
}

func TestGranuleRules(t *testing.T) {
	serial := uint32(77)
	head := buildPage(serial, 0, 0x02, 0, laceChunks(makeHeadPacket(0, 2)))
	tags := buildPage(serial, 1, 0x00, 0, laceChunks(makeTagsPacket()))
	pkt := fillPacket([]byte{toc(18, 0)}, 100, 1) // 480 samples

	cases := []struct {
		name  string
		pages [][]byte
		want  string
	}{
		{"packetless page with real granule", [][]byte{
			head, tags,
			buildPage(serial, 2, 0x00, 5, nil),
		}, "packetless"},
		{"completing page with granule -1", [][]byte{
			head, tags,
			buildPage(serial, 2, 0x00, math.MaxUint64, laceChunks(pkt)),
		}, "granule"},
		{"wrong cumulative granule", [][]byte{
			head, tags,
			buildPage(serial, 2, 0x00, 479, laceChunks(pkt)),
		}, "granule"},
		{"eos completes nothing", [][]byte{
			head, tags,
			buildPage(serial, 2, 0x04, 0, nil),
		}, "completes no packet"},
		{"eos leaves unfinished packet", [][]byte{
			head, tags,
			buildPage(serial, 2, 0x04, 0, [][]byte{make([]byte, 255)}),
		}, "completes no packet"},
		{"eos completes one but leaves unfinished", [][]byte{
			head, tags,
			buildPage(serial, 2, 0x04, 480, [][]byte{pkt, make([]byte, 255)}),
		}, "unfinished packet"},
		{"eos granule above cumulative", [][]byte{
			head, tags,
			buildPage(serial, 2, 0x04, 481, laceChunks(pkt)),
		}, "exceeds cumulative"},
		{"eos granule below preskip", [][]byte{
			buildPage(serial, 0, 0x02, 0, laceChunks(makeHeadPacket(500, 2))), tags,
			buildPage(serial, 2, 0x04, 480, laceChunks(pkt)),
		}, "below pre-skip"},
		{"serial change", [][]byte{
			head, tags,
			buildPage(serial+1, 2, 0x04, 480, laceChunks(pkt)),
		}, "serial"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := collect(concatPages(tc.pages))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
			if len(got) != 0 {
				t.Fatalf("bad stream leaked %d packets", len(got))
			}
		})
	}
}
