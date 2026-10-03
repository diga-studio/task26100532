package voxvault

import (
	"errors"
	"fmt"
	"math"
)

// maxPacketBytes is the maximum size of one Opus packet (128 KiB).
const maxPacketBytes = 128 * 1024

// granuleNone is the granule position of a page that completes no packets.
const granuleNone = math.MaxUint64

// Packet is one demuxed Opus audio packet with its position on the output
// timeline. All sample counts are in 48 kHz samples.
type Packet struct {
	Data     []byte // raw packet bytes, byte-for-byte as in the stream
	Samples  int    // decoded length of the packet
	HeadTrim int    // samples to discard from the start (pre-skip)
	TailTrim int    // samples to discard from the end (final granule)
	Start    int64  // kept interval on the output timeline: [Start, End)
	End      int64
}

type streamState int

const (
	stHead  streamState = iota // expecting the OpusHead page
	stTags                     // expecting the OpusTags page
	stAudio                    // expecting audio pages
	stDone                     // end-of-stream page processed
)

// Demuxer incrementally demuxes a single Ogg Opus logical stream.
type Demuxer struct {
	emit func(Packet)
	rd   pageReader

	state    streamState
	finished bool

	serial  uint32
	nextSeq uint32
	haveSeq bool

	preSkip    int
	cumulative int64 // decoded samples of all completed audio packets

	cur []byte // packet currently being assembled across pages, nil if none
}

// New creates a Demuxer that reports each output packet via emit.
func New(emit func(Packet)) *Demuxer {
	return &Demuxer{emit: emit}
}

// Feed consumes the next chunk of stream bytes. Chunks may split the stream
// at any byte boundary and may contain multiple pages.
func (d *Demuxer) Feed(data []byte) error {
	if d.finished {
		return errors.New("voxvault: Feed after Finish")
	}
	return d.rd.feed(data, d.processPage)
}

// Finish signals end of input and verifies the stream ended cleanly.
func (d *Demuxer) Finish() error {
	if d.finished {
		return errors.New("voxvault: Finish called twice")
	}
	d.finished = true
	if err := d.rd.finish(); err != nil {
		return err
	}
	if d.cur != nil {
		return errors.New("voxvault: truncated packet at end of input")
	}
	if d.state != stDone {
		return errors.New("voxvault: missing end-of-stream page")
	}
	return nil
}

// processPage validates one complete raw page and emits the packets it
// completes once the page CRC and granule checks pass.
func (d *Demuxer) processPage(raw []byte) error {
	hdr, err := parsePageHeader(raw)
	if err != nil {
		return err
	}
	if !verifyCRC(raw) {
		return fmt.Errorf("voxvault: bad page CRC (sequence %d)", hdr.sequence)
	}
	if !d.haveSeq {
		d.serial = hdr.serial
	} else {
		if hdr.serial != d.serial {
			return fmt.Errorf("voxvault: stream serial changed from %d to %d", d.serial, hdr.serial)
		}
		if hdr.sequence != d.nextSeq {
			return fmt.Errorf("voxvault: page sequence gap: got %d, want %d", hdr.sequence, d.nextSeq)
		}
	}
	d.nextSeq = hdr.sequence + 1
	d.haveSeq = true

	switch d.state {
	case stHead:
		return d.processHeadPage(raw, hdr)
	case stTags:
		return d.processTagsPage(raw, hdr)
	case stAudio:
		return d.processAudioPage(raw, hdr)
	default:
		return errors.New("voxvault: data after end-of-stream page")
	}
}

// singlePacketPage extracts the one packet that must solely occupy a header
// page.
func singlePacketPage(raw []byte, hdr pageHeader) ([]byte, error) {
	if hdr.continued() {
		return nil, errors.New("voxvault: header page must not be a continuation")
	}
	if hdr.granule != 0 {
		return nil, fmt.Errorf("voxvault: header page granule is %d, want 0", hdr.granule)
	}
	payload := raw[27+hdr.nSegments:]
	pos := 0
	count := 0
	endPos := 0
	for _, lace := range raw[27 : 27+hdr.nSegments] {
		n := int(lace)
		pos += n
		if n < 255 {
			count++
			endPos = pos
		}
	}
	if count != 1 || endPos != len(payload) {
		return nil, errors.New("voxvault: header page must hold exactly one complete packet")
	}
	return payload[:endPos], nil
}

func (d *Demuxer) processHeadPage(raw []byte, hdr pageHeader) error {
	if !hdr.bos() {
		return errors.New("voxvault: first page lacks beginning-of-stream flag")
	}
	pkt, err := singlePacketPage(raw, hdr)
	if err != nil {
		return err
	}
	head, err := parseOpusHead(pkt)
	if err != nil {
		return err
	}
	d.preSkip = head.preSkip
	d.state = stTags
	return nil
}

func (d *Demuxer) processTagsPage(raw []byte, hdr pageHeader) error {
	pkt, err := singlePacketPage(raw, hdr)
	if err != nil {
		return err
	}
	if len(pkt) < 8 || string(pkt[0:8]) != "OpusTags" {
		return errors.New("voxvault: missing OpusTags signature")
	}
	d.state = stAudio
	return nil
}

func (d *Demuxer) processAudioPage(raw []byte, hdr pageHeader) error {
	if hdr.continued() != (d.cur != nil) {
		return fmt.Errorf("voxvault: continuation flag mismatch (sequence %d)", hdr.sequence)
	}

	type completedPacket struct {
		data    []byte
		samples int
	}
	var completed []completedPacket
	pageSamples := 0

	payload := raw[27+hdr.nSegments:]
	pos := 0
	for _, lace := range raw[27 : 27+hdr.nSegments] {
		n := int(lace)
		chunk := payload[pos : pos+n]
		pos += n
		var pkt []byte
		switch {
		case d.cur == nil && n < 255:
			pkt = chunk // whole packet within this segment run
		default:
			d.cur = append(d.cur, chunk...)
			if len(d.cur) > maxPacketBytes {
				return fmt.Errorf("voxvault: packet exceeds %d bytes", maxPacketBytes)
			}
			if n == 255 {
				continue // packet continues in the next segment or page
			}
			pkt = d.cur
			d.cur = nil
		}
		samples, err := opusPacketSamples(pkt)
		if err != nil {
			return err
		}
		completed = append(completed, completedPacket{data: pkt, samples: samples})
		pageSamples += samples
	}

	eos := hdr.eos()
	if !eos {
		if len(completed) > 0 {
			want := uint64(d.cumulative + int64(pageSamples))
			if hdr.granule != want {
				return fmt.Errorf("voxvault: page granule %d, want cumulative %d", hdr.granule, want)
			}
		} else if hdr.granule != granuleNone {
			return fmt.Errorf("voxvault: packetless page granule %d, want -1", hdr.granule)
		}
	} else {
		if len(completed) == 0 {
			return errors.New("voxvault: end-of-stream page completes no packet")
		}
		if d.cur != nil {
			return errors.New("voxvault: end-of-stream page leaves an unfinished packet")
		}
		total := d.cumulative + int64(pageSamples)
		g := hdr.granule
		if g == granuleNone || g > uint64(total) {
			return fmt.Errorf("voxvault: end-of-stream granule %d exceeds cumulative %d", g, total)
		}
		if g < uint64(d.cumulative) {
			return fmt.Errorf("voxvault: end-of-stream granule %d below confirmed cumulative %d", g, d.cumulative)
		}
		if g < uint64(d.preSkip) {
			return fmt.Errorf("voxvault: end-of-stream granule %d below pre-skip %d", g, d.preSkip)
		}
	}

	// All page-level checks passed: publish the completed packets.
	finalGranule := int64(-1)
	if eos {
		finalGranule = int64(hdr.granule)
	}
	start := d.cumulative
	for _, cp := range completed {
		d.emitPacket(cp.data, cp.samples, start, finalGranule)
		start += int64(cp.samples)
	}
	d.cumulative = start
	if eos {
		d.state = stDone
	}
	return nil
}

// emitPacket applies pre-skip and final-granule trimming to one completed
// packet and reports it if any samples survive.
func (d *Demuxer) emitPacket(data []byte, samples int, start, finalGranule int64) {
	headTrim := int64(d.preSkip) - start
	if headTrim < 0 {
		headTrim = 0
	}
	if headTrim > int64(samples) {
		headTrim = int64(samples)
	}
	var tailTrim int64
	if finalGranule >= 0 {
		tailTrim = start + int64(samples) - finalGranule
		if tailTrim < 0 {
			tailTrim = 0
		}
		if tailTrim > int64(samples)-headTrim {
			tailTrim = int64(samples) - headTrim
		}
	}
	if int64(samples)-headTrim-tailTrim <= 0 {
		return
	}
	d.emit(Packet{
		Data:     data,
		Samples:  samples,
		HeadTrim: int(headTrim),
		TailTrim: int(tailTrim),
		Start:    start + headTrim - int64(d.preSkip),
		End:      start + int64(samples) - tailTrim - int64(d.preSkip),
	})
}
