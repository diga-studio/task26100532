package voxvault

import (
	"math/rand"
	"testing"
)

// crcRef is an independent bit-at-a-time reference for the Ogg CRC
// (poly 0x04C11DB7, init 0, no reflection, no final xor).
func crcRef(data []byte) uint32 {
	var crc uint32
	for _, b := range data {
		crc ^= uint32(b) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func TestCRCAgainstReference(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 200; i++ {
		buf := make([]byte, r.Intn(300))
		r.Read(buf)
		if got, want := crcUpdate(0, buf), crcRef(buf); got != want {
			t.Fatalf("crcUpdate=%08x, reference=%08x", got, want)
		}
	}
	// Incremental updates must match one-shot updates.
	buf := make([]byte, 1000)
	r.Read(buf)
	var c uint32
	for _, b := range buf[:137] {
		c = crcUpdate(c, []byte{b})
	}
	c = crcUpdate(c, buf[137:])
	if c != crcUpdate(0, buf) {
		t.Fatalf("incremental CRC mismatch")
	}
}
