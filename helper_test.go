package voxvault

import (
	"encoding/binary"
	"math/rand"
)

// --- Ogg stream construction ---

type oggStream struct {
	serial uint32
	seq    uint32
	buf    []byte
}

func newOggStream(serial uint32) *oggStream {
	return &oggStream{serial: serial}
}

// addRawPage appends a page from explicit lacing values and body.
func (s *oggStream) addRawPage(granule uint64, flags byte, segs []byte, body []byte) {
	hdr := make([]byte, 27)
	copy(hdr, "OggS")
	hdr[5] = flags
	binary.LittleEndian.PutUint64(hdr[6:14], granule)
	binary.LittleEndian.PutUint32(hdr[14:18], s.serial)
	binary.LittleEndian.PutUint32(hdr[18:22], s.seq)
	hdr[26] = byte(len(segs))
	pg := append(hdr, segs...)
	pg = append(pg, body...)
	crc := crcUpdate(0, pg[0:22])
	crc = crcUpdate(crc, []byte{0, 0, 0, 0})
	crc = crcUpdate(crc, pg[26:])
	binary.LittleEndian.PutUint32(pg[22:26], crc)
	s.buf = append(s.buf, pg...)
	s.seq++
}

// lace returns the lacing values for a packet of n bytes that
// terminates on the same page.
func lace(n int) []byte {
	segs := make([]byte, 0, n/255+1)
	for n >= 255 {
		segs = append(segs, 255)
		n -= 255
	}
	return append(segs, byte(n))
}

// addPacketsPage appends a page containing only whole packets.
func (s *oggStream) addPacketsPage(granule uint64, flags byte, pkts ...[]byte) {
	var segs, body []byte
	for _, p := range pkts {
		segs = append(segs, lace(len(p))...)
		body = append(body, p...)
	}
	s.addRawPage(granule, flags, segs, body)
}

// addHeadPage appends the OpusHead page.
func (s *oggStream) addHeadPage(preSkip uint16) {
	head := make([]byte, 19)
	copy(head, "OpusHead")
	head[8] = 1 // version
	head[9] = 2 // channels
	binary.LittleEndian.PutUint16(head[10:12], preSkip)
	binary.LittleEndian.PutUint32(head[12:16], 44100) // input rate: ignored
	s.addPacketsPage(0, flagBOS, head)
}

// addTagsPage appends the OpusTags page.
func (s *oggStream) addTagsPage() {
	tags := make([]byte, 8+4+4)
	copy(tags, "OpusTags")
	// vendor length 0, comment count 0
	s.addPacketsPage(0, 0, tags)
}

// --- Opus packet construction ---
//
// Config values 16..19 are CELT NB with frame durations of
// 120, 240, 480, 960 samples (2.5, 5, 10, 20 ms).

func toc(config, code int) byte { return byte(config<<3 | code) }

// fillBody writes deterministic non-zero filler into frame payloads.
func fillBody(b []byte, from, to int) {
	for i := from; i < to; i++ {
		b[i] = byte(i*31 + 7)
	}
}

// pktCode0 builds a single-frame packet.
func pktCode0(config, frame int) []byte {
	b := make([]byte, 1+frame)
	b[0] = toc(config, 0)
	fillBody(b, 1, len(b))
	return b
}

// pktCode1 builds a two-equal-frame packet.
func pktCode1(config, frameEach int) []byte {
	b := make([]byte, 1+2*frameEach)
	b[0] = toc(config, 1)
	fillBody(b, 1, len(b))
	return b
}

// pktCode2 builds a two-frame packet with explicit first size (<255).
func pktCode2(config, f1, f2 int) []byte {
	b := make([]byte, 2+f1+f2)
	b[0] = toc(config, 2)
	b[1] = byte(f1)
	fillBody(b, 2, len(b))
	return b
}

// padLenBytes encodes a padding length per RFC 6716.
func padLenBytes(pad int) []byte {
	var out []byte
	for pad > 254 {
		out = append(out, 255)
		pad -= 254
	}
	return append(out, byte(pad))
}

// pktCode3VBR builds a code-3 VBR packet; sizes lists every frame.
func pktCode3VBR(config int, sizes []int, pad int) []byte {
	count := len(sizes)
	var sizeFields []byte
	for _, s := range sizes[:count-1] {
		if s < 255 {
			sizeFields = append(sizeFields, byte(s))
		} else {
			sizeFields = append(sizeFields, 255, byte(s-255))
		}
	}
	ch := byte(0x80 | count)
	var hdr []byte
	hdr = append(hdr, sizeFields...)
	if pad > 0 {
		ch |= 0x40
		hdr = append(hdr, padLenBytes(pad)...)
	}
	total := 2 + len(hdr) + pad
	for _, s := range sizes {
		total += s
	}
	b := make([]byte, total)
	b[0] = toc(config, 3)
	b[1] = ch
	copy(b[2:], hdr)
	fillBody(b, 2+len(hdr), len(b)-pad)
	return b
}

// pktCode3CBR builds a code-3 CBR packet.
func pktCode3CBR(config, count, frameEach, pad int) []byte {
	ch := byte(count)
	var hdr []byte
	if pad > 0 {
		ch |= 0x40
		hdr = append(hdr, padLenBytes(pad)...)
	}
	total := 2 + len(hdr) + pad + count*frameEach
	b := make([]byte, total)
	b[0] = toc(config, 3)
	b[1] = ch
	copy(b[2:], hdr)
	fillBody(b, 2+len(hdr), len(b)-pad)
	return b
}

// --- feeding helpers ---

// feedAll runs the whole stream through a Demuxer in one shot.
func feedAll(t fataler, stream []byte) ([]Packet, error) {
	var out []Packet
	d := New(func(p Packet) { out = append(out, p) })
	if err := d.Feed(stream); err != nil {
		return out, err
	}
	return out, d.Finish()
}

type fataler interface {
	Helper()
	Fatalf(format string, args ...any)
}

// feedChunks feeds the stream in chunks whose boundaries come from next.
func feedChunks(stream []byte, sizes []int) ([]Packet, error) {
	var out []Packet
	d := New(func(p Packet) { out = append(out, p) })
	pos := 0
	i := 0
	for pos < len(stream) {
		n := len(stream) - pos
		if i < len(sizes) && sizes[i] < n {
			n = sizes[i]
		}
		i++
		if err := d.Feed(stream[pos : pos+n]); err != nil {
			return out, err
		}
		pos += n
	}
	return out, d.Finish()
}

// randomSplits returns k random split plans covering the stream.
func randomSplits(r *rand.Rand, n, k int) [][]int {
	plans := make([][]int, 0, k)
	for j := 0; j < k; j++ {
		var sizes []int
		total := 0
		for total < n {
			s := 1 + r.Intn(97)
			sizes = append(sizes, s)
			total += s
		}
		plans = append(plans, sizes)
	}
	return plans
}
