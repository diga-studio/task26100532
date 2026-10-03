package voxvault

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// maxPacketSamples is the maximum duration of one Opus packet: 120 ms at
// 48 kHz.
const maxPacketSamples = 5760

// maxFrameBytes is the maximum size of one compressed Opus frame.
const maxFrameBytes = 1275

// opusHead holds the fields of the OpusHead packet this library needs.
type opusHead struct {
	channels int
	preSkip  int
}

// parseOpusHead validates an OpusHead packet and extracts pre-skip.
func parseOpusHead(pkt []byte) (opusHead, error) {
	if len(pkt) != 19 {
		return opusHead{}, fmt.Errorf("voxvault: OpusHead packet has %d bytes, want 19", len(pkt))
	}
	if string(pkt[0:8]) != "OpusHead" {
		return opusHead{}, errors.New("voxvault: missing OpusHead signature")
	}
	if pkt[8] != 1 {
		return opusHead{}, fmt.Errorf("voxvault: unsupported Opus version %d", pkt[8])
	}
	ch := int(pkt[9])
	if ch != 1 && ch != 2 {
		return opusHead{}, fmt.Errorf("voxvault: unsupported channel count %d", ch)
	}
	if pkt[18] != 0 {
		return opusHead{}, fmt.Errorf("voxvault: unsupported channel mapping family %d", pkt[18])
	}
	return opusHead{
		channels: ch,
		preSkip:  int(binary.LittleEndian.Uint16(pkt[10:12])),
	}, nil
}

// frameDurationSamples returns the duration of one frame in 48 kHz samples
// for the given TOC config value (0-31).
func frameDurationSamples(config int) int {
	switch {
	case config < 12: // SILK: 10, 20, 40, 60 ms
		return [4]int{480, 960, 1920, 2880}[config%4]
	case config < 16: // Hybrid: 10, 20 ms
		return [2]int{480, 960}[config%2]
	default: // CELT: 2.5, 5, 10, 20 ms
		return [4]int{120, 240, 480, 960}[config%4]
	}
}

// opusPacketSamples validates the frame-length structure of an Opus packet
// and returns its total duration in 48 kHz samples.
func opusPacketSamples(pkt []byte) (int, error) {
	if len(pkt) < 1 {
		return 0, errors.New("voxvault: empty Opus packet")
	}
	toc := pkt[0]
	config := int(toc >> 3)
	code := int(toc & 3)
	frameDur := frameDurationSamples(config)

	var frames int
	switch code {
	case 0: // one frame
		frames = 1
		if err := checkFrameSizes([]int{len(pkt) - 1}); err != nil {
			return 0, err
		}
	case 1: // two frames, equal size
		frames = 2
		body := len(pkt) - 1
		if body%2 != 0 {
			return 0, fmt.Errorf("voxvault: code-1 packet body %d bytes is not even", body)
		}
		if err := checkFrameSizes([]int{body / 2, body / 2}); err != nil {
			return 0, err
		}
	case 2: // two frames, different sizes
		frames = 2
		if len(pkt) < 2 {
			return 0, errors.New("voxvault: truncated code-2 packet")
		}
		n1 := int(pkt[1])
		n2 := len(pkt) - 2 - n1
		if n2 < 0 {
			return 0, fmt.Errorf("voxvault: code-2 first frame of %d bytes exceeds packet", n1)
		}
		if err := checkFrameSizes([]int{n1, n2}); err != nil {
			return 0, err
		}
	case 3: // arbitrary frame count, CBR or VBR, optional padding
		var err error
		frames, err = parseCode3(pkt)
		if err != nil {
			return 0, err
		}
	}
	total := frames * frameDur
	if total > maxPacketSamples {
		return 0, fmt.Errorf("voxvault: packet duration %d samples exceeds 120 ms", total)
	}
	return total, nil
}

func checkFrameSizes(sizes []int) error {
	for _, n := range sizes {
		if n > maxFrameBytes {
			return fmt.Errorf("voxvault: frame of %d bytes exceeds %d", n, maxFrameBytes)
		}
	}
	return nil
}

// parseCode3 validates a frame-count-code-3 packet and returns its frame
// count.
func parseCode3(pkt []byte) (int, error) {
	if len(pkt) < 2 {
		return 0, errors.New("voxvault: truncated code-3 packet")
	}
	vbr := pkt[1]&0x80 != 0
	hasPad := pkt[1]&0x40 != 0
	m := int(pkt[1] & 0x3f)
	if m == 0 {
		return 0, errors.New("voxvault: code-3 packet with zero frames")
	}
	pos := 2

	pad := 0
	if hasPad {
		for {
			if pos >= len(pkt) {
				return 0, errors.New("voxvault: truncated padding length")
			}
			b := int(pkt[pos])
			pos++
			pad += b
			if pad > len(pkt)-pos {
				return 0, errors.New("voxvault: padding exceeds packet size")
			}
			if b < 255 {
				break
			}
		}
	}
	body := len(pkt) - pos - pad
	if body < 0 {
		return 0, errors.New("voxvault: padding exceeds packet size")
	}

	if !vbr {
		if body%m != 0 {
			return 0, fmt.Errorf("voxvault: CBR body %d bytes not divisible by %d frames", body, m)
		}
		if err := checkFrameSizes([]int{body / m}); err != nil {
			return 0, err
		}
		return m, nil
	}

	sizes := make([]int, 0, m)
	sum := 0
	for i := 0; i < m-1; i++ {
		if pos >= len(pkt)-pad {
			return 0, errors.New("voxvault: truncated VBR frame sizes")
		}
		b := int(pkt[pos])
		pos++
		var n int
		if b < 252 {
			n = b
		} else {
			if pos >= len(pkt)-pad {
				return 0, errors.New("voxvault: truncated VBR frame sizes")
			}
			n = 4*int(pkt[pos]) + b
			pos++
		}
		sizes = append(sizes, n)
		sum += n
	}
	last := len(pkt) - pos - pad - sum
	if last < 0 {
		return 0, errors.New("voxvault: VBR frame sizes exceed packet")
	}
	sizes = append(sizes, last)
	if err := checkFrameSizes(sizes); err != nil {
		return 0, err
	}
	return m, nil
}
