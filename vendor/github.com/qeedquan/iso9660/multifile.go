package iso9660

import (
	"fmt"
	"io"
	"os"
)

// MultiFile creates concatenation out of a list files
// and treats them as one contiguous file.
type MultiFile struct {
	files []*os.File
	offs  []int64
	size  int64
	pos   int64
}

// NewMultiFile makes a MultiFile out of a set of the OS files.
// It will treat the inorder list of files as one contiguous buffer.
func NewMultiFile(name ...string) (*MultiFile, error) {
	r := &MultiFile{
		files: make([]*os.File, len(name)),
		offs:  make([]int64, len(name)),
	}

	var err error
	defer func() {
		if err != nil {
			r.Close()
		}
	}()

	for i, name := range name {
		var f *os.File
		var fi os.FileInfo

		f, err = os.Open(name)
		if err != nil {
			return nil, err
		}

		fi, err = f.Stat()
		if err != nil {
			return nil, fmt.Errorf("%v: %v", name)
		}

		r.files[i] = f
		r.size += fi.Size()
		if i > 0 {
			r.offs[i] = r.offs[i-1] + fi.Size()
		}
		if r.offs[i] < 0 {
			return nil, fmt.Errorf("files too large")
		}
	}

	return r, nil
}

// Seek seeks to an offset relative to whence.
func (r *MultiFile) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case os.SEEK_SET:

	case os.SEEK_CUR:
		off += r.pos

	case os.SEEK_END:
		off += r.size

	default:
		return 0, os.ErrInvalid
	}

	if off < 0 {
		return 0, os.ErrInvalid
	}

	r.pos = off

	return r.pos, nil
}

// Read reads data from the buffer.
func (r *MultiFile) Read(p []byte) (int, error) {
	n, err := r.ReadAt(p, r.pos)
	if err != nil {
		return n, err
	}

	r.pos += int64(n)
	return n, err
}

// ReadAt reads the data at an offset.
func (r *MultiFile) ReadAt(p []byte, off int64) (int, error) {
	f := r.fileAt(off)
	if f == nil {
		return 0, io.EOF
	}

	n := 0
	for {
		nr, err := f.ReadAt(p[n:], off)
		n += nr

		switch {
		case err != nil && err != io.EOF:
			return n, err

		case err == io.EOF:
			if n == len(p) {
				return n, io.EOF
			}

			off += int64(nr)
			xf := r.fileAt(off)
			if xf == nil || xf == f {
				return n, io.EOF
			}
			f = xf

		default:
			return n, nil
		}
	}
}

// Close closes the file.
func (r *MultiFile) Close() error {
	for i := range r.files {
		r.files[i].Close()
	}
	return nil
}

// Size returns the length of all the files combined.
func (r *MultiFile) Size() int64 {
	return r.size
}

// fileAt returns a file backing a position, or nil if the position
// is out of scope.
func (r *MultiFile) fileAt(pos int64) *os.File {
	if len(r.files) == 0 || pos < 0 {
		return nil
	}

	lo, hi := 0, len(r.files)-1
	for lo < hi {
		mid := lo + (hi-lo)/2
		if r.offs[mid] < pos {
			lo = mid + 1
		} else {
			hi = mid
		}
	}

	if lo > 0 {
		lo--
	}

	if r.offs[lo] > pos {
		return nil
	}

	return r.files[lo]
}
