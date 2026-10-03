package voxvault

import (
	"errors"
	"fmt"
)

// Packet is one audio packet emitted by the Demuxer.
type Packet struct {
	Data      []byte // raw packet bytes, byte-for-byte as in the stream
	Samples   int    // decoded sample count at 48 kHz
	TrimStart int    // samples to discard from the head (pre-skip)
	TrimEnd   int    // samples to discard from the tail (final granule)
	Start     int64  // kept interval on the zero-based output timeline: [Start, End)
	End       int64
}

type state int

const (
	stHead  state = iota // expecting the OpusHead page
	stTags               // expecting the OpusTags page
	stAudio              // expecting audio pages
	stDone               // EOS page processed
)

const granuleNone = 0xFFFFFFFFFFFFFFFF

// Demuxer incrementally unpacks a single Ogg Opus logical stream.
// It handles encapsulation, packet structure and the sample timeline
// only; compressed audio is never decoded.
type Demuxer struct {
	emit func(Packet)
	err  error

	state      state
	buf        []byte // staging for the current (possibly partial) page
	serial     uint32
	haveSerial bool
	nextSeq    uint32
	continued  bool   // previous page ended mid-packet
	pkt        []byte // current partial audio packet

	preSkip      int
	confirmed    int64 // decoded samples of all complete packets so far
	finalGranule int64
}

// New creates a Demuxer that delivers each output packet to emit.
func New(emit func(Packet)) *Demuxer {
	return &Demuxer{emit: emit, state: stHead}
}

// Feed consumes a chunk of the stream. Chunks may be split at any byte
// and may contain any number of pages. Packets completed by validated
// pages are delivered to the emit callback before Feed returns.
func (d *Demuxer) Feed(data []byte) error {
	if d.err != nil {
		return d.err
	}
	if err := d.feed(data); err != nil {
		d.err = err
		return err
	}
	return nil
}

// Finish reports success only if the stream ended cleanly with an EOS
// page, leaving no truncated page or packet behind.
func (d *Demuxer) Finish() error {
	if d.err != nil {
		return d.err
	}
	switch {
	case d.state == stDone:
		return nil
	case len(d.buf) > 0:
		return d.latch(errors.New("voxvault: truncated page at end of stream"))
	case len(d.pkt) > 0:
		return d.latch(errors.New("voxvault: truncated packet at end of stream"))
	case d.state != stAudio:
		return d.latch(errors.New("voxvault: stream ended before headers completed"))
	default:
		return d.latch(errors.New("voxvault: missing EOS page"))
	}
}

func (d *Demuxer) latch(err error) error {
	d.err = err
	return err
}

func (d *Demuxer) feed(data []byte) error {
	for len(data) > 0 {
		if d.state == stDone {
			return errors.New("voxvault: data after end of stream")
		}
		need, err := d.pageNeed()
		if err != nil {
			return err
		}
		take := need - len(d.buf)
		if take > len(data) {
			take = len(data)
		}
		d.buf = append(d.buf, data[:take]...)
		data = data[take:]
		if len(d.buf) < need {
			break
		}
		full, err := d.pageNeed()
		if err != nil {
			return err
		}
		if len(d.buf) < full {
			continue
		}
		p, err := parsePage(d.buf)
		if err != nil {
			return err
		}
		if err := d.processPage(p); err != nil {
			return err
		}
		d.buf = d.buf[:0]
	}
	return nil
}

// pageNeed returns the number of bytes that must be staged in d.buf
// before the next parsing step. The buffer never exceeds one page.
func (d *Demuxer) pageNeed() (int, error) {
	if len(d.buf) < 27 {
		return 27, nil
	}
	if string(d.buf[0:4]) != "OggS" {
		return 0, errBadCapture
	}
	if d.buf[4] != 0 {
		return 0, fmt.Errorf("voxvault: unsupported page version %d", d.buf[4])
	}
	nsegs := int(d.buf[26])
	if len(d.buf) < 27+nsegs {
		return 27 + nsegs, nil
	}
	return 27 + nsegs + bodyLen(d.buf[27:27+nsegs]), nil
}

func (d *Demuxer) processPage(p *page) error {
	if !d.haveSerial {
		d.serial = p.serial
		d.haveSerial = true
	} else {
		if p.serial != d.serial {
			return fmt.Errorf("voxvault: stream serial changed (seq %d)", p.seq)
		}
		if p.seq != d.nextSeq {
			return fmt.Errorf("voxvault: page sequence gap: want %d, got %d", d.nextSeq, p.seq)
		}
	}
	d.nextSeq = p.seq + 1 // 32-bit wrap is intentional
	cont := p.headerType&flagContinued != 0
	if cont != d.continued {
		return fmt.Errorf("voxvault: continuation flag mismatch (seq %d)", p.seq)
	}
	if d.state != stHead && p.headerType&flagBOS != 0 {
		return fmt.Errorf("voxvault: unexpected BOS flag (seq %d)", p.seq)
	}
	switch d.state {
	case stHead:
		return d.processHeadPage(p)
	case stTags:
		return d.processTagsPage(p)
	case stAudio:
		return d.processAudioPage(p)
	default:
		return errors.New("voxvault: data after end of stream")
	}
}

// singlePacketPage extracts the one packet that must solely occupy a
// header page.
func singlePacketPage(p *page) ([]byte, error) {
	ends := 0
	for i, lace := range p.segments {
		if lace < 255 {
			ends++
			if i != len(p.segments)-1 {
				return nil, errors.New("voxvault: header page holds more than one packet")
			}
		}
	}
	if ends == 0 {
		return nil, errors.New("voxvault: header packet spans pages")
	}
	return p.body, nil
}

func (d *Demuxer) processHeadPage(p *page) error {
	if p.headerType&flagBOS == 0 {
		return errors.New("voxvault: first page lacks BOS flag")
	}
	if p.headerType&flagEOS != 0 {
		return errors.New("voxvault: EOS on head page")
	}
	if p.granule != 0 {
		return errors.New("voxvault: head page granule must be 0")
	}
	data, err := singlePacketPage(p)
	if err != nil {
		return err
	}
	head, err := parseOpusHead(data)
	if err != nil {
		return err
	}
	d.preSkip = head.preSkip
	d.state = stTags
	return nil
}

func (d *Demuxer) processTagsPage(p *page) error {
	if p.headerType&flagEOS != 0 {
		return errors.New("voxvault: EOS before any audio packet")
	}
	if p.granule != 0 {
		return errors.New("voxvault: tags page granule must be 0")
	}
	data, err := singlePacketPage(p)
	if err != nil {
		return err
	}
	if len(data) < 8 || string(data[0:8]) != "OpusTags" {
		return errors.New("voxvault: missing OpusTags signature")
	}
	d.state = stAudio
	return nil
}

type completedPacket struct {
	data    []byte
	samples int
}

func (d *Demuxer) processAudioPage(p *page) error {
	eos := p.headerType&flagEOS != 0

	var done []completedPacket
	pos := 0
	for _, lace := range p.segments {
		n := int(lace)
		d.pkt = append(d.pkt, p.body[pos:pos+n]...)
		pos += n
		if len(d.pkt) > maxPacketBytes {
			return fmt.Errorf("voxvault: packet exceeds %d bytes", maxPacketBytes)
		}
		if lace < 255 {
			samples, err := packetDuration(d.pkt)
			if err != nil {
				return err
			}
			done = append(done, completedPacket{data: d.pkt, samples: samples})
			d.pkt = nil
		}
	}
	if len(p.segments) > 0 {
		d.continued = p.segments[len(p.segments)-1] == 255
	}

	var pageSamples int64
	for _, c := range done {
		pageSamples += int64(c.samples)
	}
	newConfirmed := d.confirmed + pageSamples

	if eos {
		if len(done) == 0 {
			return errors.New("voxvault: final page completes no packet")
		}
		if len(d.pkt) > 0 {
			return errors.New("voxvault: final page leaves an incomplete packet")
		}
		g := int64(p.granule)
		if g < d.confirmed {
			return fmt.Errorf("voxvault: final granule %d below confirmed %d", g, d.confirmed)
		}
		if g > newConfirmed {
			return fmt.Errorf("voxvault: final granule %d exceeds decoded %d", g, newConfirmed)
		}
		if g < int64(d.preSkip) {
			return fmt.Errorf("voxvault: final granule %d below pre-skip %d", g, d.preSkip)
		}
		d.finalGranule = g
	} else if len(done) == 0 {
		if p.granule != granuleNone {
			return fmt.Errorf("voxvault: page completing no packet must have granule -1, got %d", p.granule)
		}
	} else if p.granule != uint64(newConfirmed) {
		return fmt.Errorf("voxvault: granule %d does not match decoded total %d", p.granule, newConfirmed)
	}

	// The page is fully validated (CRC, sequence, granule): publish.
	start := d.confirmed
	for _, c := range done {
		end := start + int64(c.samples)
		d.emitPacket(c, start, end, eos)
		start = end
	}
	d.confirmed = newConfirmed
	if eos {
		d.state = stDone
	}
	return nil
}

func (d *Demuxer) emitPacket(c completedPacket, start, end int64, final bool) {
	keptStart := start
	if ps := int64(d.preSkip); keptStart < ps {
		keptStart = ps
	}
	keptEnd := end
	if final && keptEnd > d.finalGranule {
		keptEnd = d.finalGranule
	}
	if keptStart >= keptEnd {
		return // fully trimmed
	}
	d.emit(Packet{
		Data:      c.data,
		Samples:   c.samples,
		TrimStart: int(keptStart - start),
		TrimEnd:   int(end - keptEnd),
		Start:     keptStart - int64(d.preSkip),
		End:       keptEnd - int64(d.preSkip),
	})
}
