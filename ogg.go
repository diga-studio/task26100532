package voxvault

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// pageHeader holds the parsed fields of one Ogg page header.
type pageHeader struct {
	headerType byte
	granule    uint64
	serial     uint32
	sequence   uint32
	nSegments  int
	payloadLen int
}

func (h pageHeader) continued() bool { return h.headerType&0x01 != 0 }
func (h pageHeader) bos() bool       { return h.headerType&0x02 != 0 }
func (h pageHeader) eos() bool       { return h.headerType&0x04 != 0 }

// pageReader incrementally reassembles whole Ogg pages from a byte stream.
// It buffers at most one page (27 + 255 + 255*255 bytes).
type pageReader struct {
	buf []byte
}

// feed consumes data and invokes onPage with the raw bytes of each complete
// page. The slice passed to onPage is not reused by pageReader.
func (r *pageReader) feed(data []byte, onPage func(raw []byte) error) error {
	for len(data) > 0 {
		want := r.wanted()
		n := want - len(r.buf)
		if n > len(data) {
			n = len(data)
		}
		r.buf = append(r.buf, data[:n]...)
		data = data[n:]
		if len(r.buf) == r.wanted() {
			raw := r.buf
			r.buf = nil
			if err := onPage(raw); err != nil {
				return err
			}
		}
	}
	return nil
}

// wanted returns the total size of the page currently being assembled, which
// becomes known in stages: fixed header, segment table, then payload.
func (r *pageReader) wanted() int {
	const fixed = 27
	if len(r.buf) < fixed {
		return fixed
	}
	nSeg := int(r.buf[26])
	if len(r.buf) < fixed+nSeg {
		return fixed + nSeg
	}
	total := fixed + nSeg
	for _, lace := range r.buf[fixed : fixed+nSeg] {
		total += int(lace)
	}
	return total
}

// finish reports an error if a partial page remains buffered.
func (r *pageReader) finish() error {
	if len(r.buf) > 0 {
		return fmt.Errorf("voxvault: truncated page: %d bytes of an incomplete page at end of input", len(r.buf))
	}
	return nil
}

// parsePageHeader validates and parses the fixed header plus segment table of
// a complete raw page.
func parsePageHeader(raw []byte) (pageHeader, error) {
	if len(raw) < 27 {
		return pageHeader{}, errors.New("voxvault: page shorter than fixed header")
	}
	if string(raw[0:4]) != "OggS" {
		return pageHeader{}, errors.New("voxvault: missing OggS capture pattern")
	}
	if raw[4] != 0 {
		return pageHeader{}, fmt.Errorf("voxvault: unsupported stream structure version %d", raw[4])
	}
	nSeg := int(raw[26])
	if len(raw) < 27+nSeg {
		return pageHeader{}, errors.New("voxvault: page shorter than segment table")
	}
	payload := 0
	for _, lace := range raw[27 : 27+nSeg] {
		payload += int(lace)
	}
	if len(raw) != 27+nSeg+payload {
		return pageHeader{}, errors.New("voxvault: page size does not match segment table")
	}
	return pageHeader{
		headerType: raw[5],
		granule:    binary.LittleEndian.Uint64(raw[6:14]),
		serial:     binary.LittleEndian.Uint32(raw[14:18]),
		sequence:   binary.LittleEndian.Uint32(raw[18:22]),
		nSegments:  nSeg,
		payloadLen: payload,
	}, nil
}

// verifyCRC checks the Ogg CRC32 of a complete raw page.
func verifyCRC(raw []byte) bool {
	if len(raw) < 27 {
		return false
	}
	crc := make([]byte, len(raw))
	copy(crc, raw)
	stored := binary.LittleEndian.Uint32(crc[22:26])
	crc[22], crc[23], crc[24], crc[25] = 0, 0, 0, 0
	return oggCRC32(crc) == stored
}

var oggCRCTable = func() [256]uint32 {
	var tab [256]uint32
	for i := 0; i < 256; i++ {
		r := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if r&0x80000000 != 0 {
				r = (r << 1) ^ 0x04c11db7
			} else {
				r <<= 1
			}
		}
		tab[i] = r
	}
	return tab
}()

// oggCRC32 computes the Ogg variant of CRC-32 (poly 0x04c11db7, no
// reflection, init 0, no final xor).
func oggCRC32(data []byte) uint32 {
	var crc uint32
	for _, b := range data {
		crc = (crc << 8) ^ oggCRCTable[byte(crc>>24)^b]
	}
	return crc
}
