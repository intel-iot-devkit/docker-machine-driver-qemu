package iso9660

import (
	"fmt"
	"image"
	"io"
)

const (
	minSectorLength = 2048
	maxSectorLength = 2456
	minSectors      = 16
	frameSize       = 24
	magic           = "CD001"
)

type sectorFormat struct {
	size   int64
	offset int64
	start  int64
}

var sectorFormats = []sectorFormat{
	{2048, 0, 0},  // ISO 2048
	{2336, 0, 0},  // RAW 2336
	{2352, 0, 24}, // RAW 2352
	{2448, 0, 24}, // RAWQ 2448

	{2048, 150 * 2048, 0},  // NERO ISO 2048
	{2352, 150 * 2048, 24}, // NERO RAW 2352
	{2448, 150 * 2048, 24}, // NERO RAWQ 2448

	{2048, -8, 0},  // ISO 2048
	{2352, -8, 24}, // RAW 2352
	{2448, -8, 24}, // RAWQ 2448
}

// Buffer is an interface providing a method to get the length
// of the buffer and random access to it.
type Buffer interface {
	io.ReaderAt
	io.Closer
	Size() int64
}

// An Image represents a CD image that treats a Buffer
// as if it was composed of sectors.
type Image struct {
	Buffer
	sector sectorFormat
}

// NewImage makes an image out of a Buffer.
// Image turns a buffer into a run of sectors,
// like a conventional CD image would be composed of.
func NewImage(b Buffer) (*Image, error) {
	m := &Image{Buffer: b}

	var p [maxSectorLength]byte
	for _, sector := range sectorFormats {
		m.sector = sector
		_, err := m.ReadSector(16, p[:])
		if err != nil {
			return nil, fmt.Errorf("error reading image: %v", err)
		}

		if string(p[1:6]) == magic {
			return m, nil
		}
	}

	return nil, image.ErrFormat
}

// ReadSector reads the sector lba and stores it into the buffer.
// Use the SectorSize to determine how big the buffer should be.
func (m *Image) ReadSector(lba int64, b []byte) (n int, err error) {
	length := int64(len(b))
	if length > m.sector.size {
		length = m.sector.size
	}

	pos := (int64(lba) * m.sector.size) + m.sector.start + m.sector.offset
	n, err = m.ReadAt(b[:length], int64(pos))
	if err != nil {
		return
	}

	switch {
	case int64(len(b)) < m.sector.size:
		err = io.ErrShortBuffer
	case n < minSectorLength:
		err = io.ErrUnexpectedEOF
	}

	return
}

// NumSectors returns the number of sectors the image contains.
func (m *Image) NumSectors() int64 {
	length := m.Size()
	n := length / m.sector.size
	if length%m.sector.size != 0 {
		n++
	}
	return n
}

// SectorSize returns the sector size of the image.
func (m *Image) SectorSize() int64 {
	return m.sector.size
}
