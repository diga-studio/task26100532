package voxvault

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	pageMaxSize   = 27 + 255 + 255*255
	flagContinued = 0x01
	flagBOS       = 0x02
	flagEOS       = 0x04
)

var errBadCapture = errors.New("voxvault: bad OggS capture pattern")

type page struct {
	headerType byte
	granule    uint64
	serial     uint32
	seq        uint32
	segments   []byte // lacing values
	body       []byte // page payload (all segments concatenated)
}

// pageSize returns the total byte length of a page whose 27-byte
// fixed header is in hdr, or an error if the header is malformed.
func pageSize(hdr []byte) (int, error) {
	if len(hdr) < 27 {
		return 0, errors.New("voxvault: short page header")
	}
	if string(hdr[0:4]) != "OggS" {
		return 0, errBadCapture
	}
	if hdr[4] != 0 {
		return 0, fmt.Errorf("voxvault: unsupported page version %d", hdr[4])
	}
	nsegs := int(hdr[26])
	if len(hdr) < 27+nsegs {
		return 0, errors.New("voxvault: short segment table")
	}
	body := 0
	for _, l := range hdr[27 : 27+nsegs] {
		body += int(l)
	}
	return 27 + nsegs + body, nil
}

// parsePage validates the CRC and decodes a complete page held in buf.
// buf must contain exactly one full page.
func parsePage(buf []byte) (*page, error) {
	if len(buf) < 27 {
		return nil, errors.New("voxvault: short page")
	}
	nsegs := int(buf[26])
	if len(buf) != 27+nsegs+bodyLen(buf[27:27+nsegs]) {
		return nil, errors.New("voxvault: page length mismatch")
	}
	want := binary.LittleEndian.Uint32(buf[22:26])
	crc := crcUpdate(0, buf[0:22])
	crc = crcUpdate(crc, []byte{0, 0, 0, 0})
	crc = crcUpdate(crc, buf[26:])
	if crc != want {
		return nil, fmt.Errorf("voxvault: page CRC mismatch (seq %d)", binary.LittleEndian.Uint32(buf[18:22]))
	}
	p := &page{
		headerType: buf[5],
		granule:    binary.LittleEndian.Uint64(buf[6:14]),
		serial:     binary.LittleEndian.Uint32(buf[14:18]),
		seq:        binary.LittleEndian.Uint32(buf[18:22]),
		segments:   buf[27 : 27+nsegs],
		body:       buf[27+nsegs:],
	}
	return p, nil
}

func bodyLen(segs []byte) int {
	n := 0
	for _, l := range segs {
		n += int(l)
	}
	return n
}
