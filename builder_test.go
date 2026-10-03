package voxvault

import (
	"encoding/binary"
	"math"
)

// laceChunks splits a packet into Ogg lacing segments: 255-byte chunks plus a
// terminating chunk, which is empty when the length is a multiple of 255.
func laceChunks(data []byte) [][]byte {
	var chunks [][]byte
	for len(data) >= 255 {
		chunks = append(chunks, data[:255])
		data = data[255:]
	}
	return append(chunks, data)
}

// buildPage assembles one raw Ogg page from per-segment payload chunks and
// fills in a valid CRC.
func buildPage(serial, seq uint32, htype byte, granule uint64, chunks [][]byte) []byte {
	payload := 0
	for _, c := range chunks {
		payload += len(c)
	}
	buf := make([]byte, 0, 27+len(chunks)+payload)
	buf = append(buf, 'O', 'g', 'g', 'S', 0, htype)
	var tmp [8]byte
	binary.LittleEndian.PutUint64(tmp[:], granule)
	buf = append(buf, tmp[:]...)
	binary.LittleEndian.PutUint32(tmp[:4], serial)
	buf = append(buf, tmp[:4]...)
	binary.LittleEndian.PutUint32(tmp[:4], seq)
	buf = append(buf, tmp[:4]...)
	buf = append(buf, 0, 0, 0, 0) // CRC placeholder
	buf = append(buf, byte(len(chunks)))
	for _, c := range chunks {
		buf = append(buf, byte(len(c)))
	}
	for _, c := range chunks {
		buf = append(buf, c...)
	}
	binary.LittleEndian.PutUint32(buf[22:26], oggCRC32(buf))
	return buf
}

// fixCRC recomputes the CRC of a mutated raw page.
func fixCRC(page []byte) {
	page[22], page[23], page[24], page[25] = 0, 0, 0, 0
	binary.LittleEndian.PutUint32(page[22:26], oggCRC32(page))
}

func makeHeadPacket(preSkip, channels int) []byte {
	pkt := make([]byte, 19)
	copy(pkt, "OpusHead")
	pkt[8] = 1
	pkt[9] = byte(channels)
	binary.LittleEndian.PutUint16(pkt[10:12], uint16(preSkip))
	binary.LittleEndian.PutUint32(pkt[12:16], 48000)
	return pkt
}

func makeTagsPacket() []byte {
	return append([]byte("OpusTags"), make([]byte, 16)...)
}

// streamCfg configures validStreamPages.
type streamCfg struct {
	preSkip      int
	channels     int
	serial       uint32
	startSeq     uint32
	packets      [][]byte
	samples      []int // decoded samples per packet, by construction
	segsPerPage  int   // max lacing segments per audio page
	finalGranule int64 // granule of the EOS page; -1 means total cumulative
}

// validStreamPages builds a spec-compliant Ogg Opus stream as a list of raw
// pages. Audio packets are packed into pages of at most segsPerPage segments,
// splitting packets across pages when necessary.
func validStreamPages(cfg streamCfg) [][]byte {
	if cfg.channels == 0 {
		cfg.channels = 2
	}
	if cfg.segsPerPage <= 0 {
		cfg.segsPerPage = 255
	}
	seq := cfg.startSeq
	next := func() uint32 { s := seq; seq++; return s }

	pages := [][]byte{
		buildPage(cfg.serial, next(), 0x02, 0, laceChunks(makeHeadPacket(cfg.preSkip, cfg.channels))),
		buildPage(cfg.serial, next(), 0x00, 0, laceChunks(makeTagsPacket())),
	}

	var cum int64
	var pageChunks [][]byte
	pageCont := false  // current page started mid-packet
	pageDone := false  // current page completed at least one packet
	midPacket := false // previous segment ended with lacing 255
	flush := func(eos bool, granule uint64) {
		htype := byte(0)
		if pageCont {
			htype |= 0x01
		}
		if eos {
			htype |= 0x04
		}
		pages = append(pages, buildPage(cfg.serial, next(), htype, granule, pageChunks))
		pageChunks = nil
		pageCont = false
		pageDone = false
	}

	for i, pkt := range cfg.packets {
		for _, chunk := range laceChunks(pkt) {
			if len(pageChunks) == cfg.segsPerPage {
				g := uint64(math.MaxUint64)
				if pageDone {
					g = uint64(cum)
				}
				flush(false, g)
				pageCont = midPacket
			}
			pageChunks = append(pageChunks, chunk)
			if len(chunk) < 255 {
				cum += int64(cfg.samples[i])
				pageDone = true
				midPacket = false
			} else {
				midPacket = true
			}
		}
	}

	final := uint64(cum)
	if cfg.finalGranule >= 0 {
		final = uint64(cfg.finalGranule)
	}
	flush(true, final)
	return pages
}

func concatPages(pages [][]byte) []byte {
	total := 0
	for _, p := range pages {
		total += len(p)
	}
	out := make([]byte, 0, total)
	for _, p := range pages {
		out = append(out, p...)
	}
	return out
}

// collect runs a Demuxer over chunks and returns all emitted packets.
func collect(chunks ...[]byte) ([]Packet, error) {
	var out []Packet
	d := New(func(p Packet) { out = append(out, p) })
	for _, c := range chunks {
		if err := d.Feed(c); err != nil {
			return out, err
		}
	}
	return out, d.Finish()
}
