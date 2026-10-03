package voxvault

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	maxPacketBytes   = 128 * 1024
	maxPacketSamples = 5760 // 120 ms at 48 kHz
	maxFrameBytes    = 1275
)

type headInfo struct {
	channels int
	preSkip  int
}

func parseOpusHead(data []byte) (headInfo, error) {
	var h headInfo
	if len(data) != 19 {
		return h, fmt.Errorf("voxvault: OpusHead must be 19 bytes, got %d", len(data))
	}
	if string(data[0:8]) != "OpusHead" {
		return h, errors.New("voxvault: missing OpusHead signature")
	}
	if data[8] != 1 {
		return h, fmt.Errorf("voxvault: unsupported OpusHead version %d", data[8])
	}
	if data[9] != 1 && data[9] != 2 {
		return h, fmt.Errorf("voxvault: unsupported channel count %d", data[9])
	}
	if data[18] != 0 {
		return h, fmt.Errorf("voxvault: unsupported mapping family %d", data[18])
	}
	h.channels = int(data[9])
	h.preSkip = int(binary.LittleEndian.Uint16(data[10:12]))
	// data[12:16] input sample rate and data[16:18] gain are ignored:
	// all timing is computed at 48 kHz.
	return h, nil
}

// frameDuration returns the per-frame duration in 48 kHz samples for
// the given TOC config field.
func frameDuration(config int) int {
	switch {
	case config < 12:
		return [4]int{480, 960, 1920, 2880}[config&3]
	case config < 16:
		return [2]int{480, 960}[config&1]
	default:
		return [4]int{120, 240, 480, 960}[config&3]
	}
}

// readSize decodes a 1- or 2-byte frame-size field (RFC 6716 §3.2.1).
func readSize(data []byte, pos int) (size, next int, err error) {
	if pos >= len(data) {
		return 0, 0, errors.New("voxvault: truncated frame size")
	}
	b := int(data[pos])
	if b < 255 {
		return b, pos + 1, nil
	}
	if pos+1 >= len(data) {
		return 0, 0, errors.New("voxvault: truncated frame size")
	}
	return 255 + int(data[pos+1]), pos + 2, nil
}

// packetDuration returns the number of decoded samples (at 48 kHz)
// in a single Opus packet, validating its frame-length structure.
// The compressed frame payloads themselves are not inspected.
func packetDuration(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, errors.New("voxvault: empty Opus packet")
	}
	if len(data) > maxPacketBytes {
		return 0, fmt.Errorf("voxvault: packet too large: %d bytes", len(data))
	}
	toc := data[0]
	dur := frameDuration(int(toc >> 3))
	code := int(toc & 3)

	validate := func(count int, sizes []int, last int) (int, error) {
		if count < 1 || count > 48 {
			return 0, fmt.Errorf("voxvault: invalid frame count %d", count)
		}
		if count*dur > maxPacketSamples {
			return 0, fmt.Errorf("voxvault: packet duration %d samples exceeds 120ms", count*dur)
		}
		for _, s := range sizes {
			if s < 0 || s > maxFrameBytes {
				return 0, fmt.Errorf("voxvault: invalid frame size %d", s)
			}
		}
		if last < 0 || last > maxFrameBytes {
			return 0, fmt.Errorf("voxvault: invalid frame size %d", last)
		}
		return count * dur, nil
	}

	switch code {
	case 0:
		return validate(1, nil, len(data)-1)
	case 1:
		payload := len(data) - 1
		if payload%2 != 0 {
			return 0, errors.New("voxvault: code-1 packet has odd payload")
		}
		return validate(2, nil, payload/2)
	case 2:
		sz, pos, err := readSize(data, 1)
		if err != nil {
			return 0, err
		}
		return validate(2, []int{sz}, len(data)-pos-sz)
	default: // code 3
		if len(data) < 2 {
			return 0, errors.New("voxvault: truncated code-3 packet")
		}
		ch := data[1]
		vbr := ch&0x80 != 0
		hasPad := ch&0x40 != 0
		count := int(ch & 0x3f)
		pos := 2
		var sizes []int
		sum := 0
		if vbr {
			for i := 0; i < count-1; i++ {
				sz, next, err := readSize(data, pos)
				if err != nil {
					return 0, err
				}
				sizes = append(sizes, sz)
				sum += sz
				pos = next
			}
		}
		pad := 0
		if hasPad {
			for {
				if pos >= len(data) {
					return 0, errors.New("voxvault: truncated padding length")
				}
				b := int(data[pos])
				pos++
				if b == 255 {
					pad += 254
				} else {
					pad += b
					break
				}
			}
		}
		remain := len(data) - pos - pad
		if remain < 0 {
			return 0, errors.New("voxvault: padding exceeds packet")
		}
		if vbr {
			return validate(count, sizes, remain-sum)
		}
		if count < 1 {
			return 0, fmt.Errorf("voxvault: invalid frame count %d", count)
		}
		if remain%count != 0 {
			return 0, errors.New("voxvault: CBR payload not divisible by frame count")
		}
		return validate(count, nil, remain/count)
	}
}
