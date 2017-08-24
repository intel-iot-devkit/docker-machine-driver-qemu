// Package ISO9660 implements a basic reader for the ISO9660 filesystem.
// Extensions such as Joliet or Rock Ridge is not implemented.
package iso9660

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	stdpath "path"
	"strings"
	"time"
)

var (
	ErrIsDir  = errors.New("is a directory")
	ErrNotDir = errors.New("not a directory")
)

type volumeDescriptor struct {
	Type    uint8
	Ident   [5]uint8
	Version uint8
}

type primaryVolumeDescriptor struct {
	BlockSize     int64
	Root          directory
	PathTableSize int64
	PathTable     [2]int64
}

type directory struct {
	Siz        uint8
	ExSize     uint8
	LBA        uint32
	Length     uint32
	Time       [7]uint8
	Flags      uint8
	Interleave struct {
		Size uint8
		Gap  uint8
	}
	Seq uint16
	Nam string
}

const (
	modeHidden = 1 << iota
	modeDir
	modeAssociated
	modeExtended
	modePerm
	_
	_
	modeSpanExtents
)

type path struct {
	Size   uint16
	ExSize uint8
	LBA    uint32
	Parent uint16
	Name   string
}

// Reader provides an interface for reading sectors, it simulates what a CD drive provides.
type Reader interface {
	NumSectors() int64
	SectorSize() int64
	ReadSector(lba int64, b []byte) (int, error)
	io.Closer
}

// FileSystem represents a ISO9660 file system.
type FileSystem struct {
	r        Reader
	pvd, svd primaryVolumeDescriptor
	paths    []path
	dirs     map[string]bool
	files    map[string]File
	curdir   string
}

// NewFileSystem makes a FileSystem from a Reader
func NewFileSystem(r Reader) (*FileSystem, error) {
	fs := &FileSystem{r: r, curdir: "/"}

	err := fs.findVolumes()
	if err != nil {
		return nil, fmt.Errorf("failed to read volume descriptor: %v", err)
	}

	fs.buildCache()

	return fs, nil
}

// Open creates an ISO9660 filesystem out of OS files.
func Open(name ...string) (*FileSystem, error) {
	m, err := NewMultiFile(name...)
	if err != nil {
		return nil, err
	}

	i, err := NewImage(m)
	if err != nil {
		return nil, err
	}

	return NewFileSystem(i)
}

// Close closes the reader that the filesystem is using.
func (fs *FileSystem) Close() error {
	return fs.r.Close()
}

// Chdir changes the filesystem current working directory.
// There will be an error returned if it is not a valid directory.
func (fs *FileSystem) Chdir(dir string) error {
	errNotDir := &os.PathError{"chdir", dir, ErrNotDir}
	errNotExist := &os.PathError{"chdir", dir, os.ErrNotExist}

	dir = strings.ToUpper(stdpath.Join(fs.curdir, dir))
	if dir == "." || dir == "" {
		dir = "/"
	}

	if _, exist := fs.files[dir]; exist {
		return errNotDir
	}

	// worst case, we have to walk because the path tables
	// can be incomplete
	if !fs.dirs[dir] {
		xdir := fs.curdir
		fs.curdir = dir

		f, err := fs.Open(".")
		fs.curdir = xdir

		if err != nil {
			return errNotExist
		}

		if !f.fi.IsDir() {
			return errNotDir
		}

		fs.dirs[dir] = true
	}

	fs.curdir = dir
	return nil
}

// Getwd gets the current working directory.
func (fs *FileSystem) Getwd() (string, error) {
	return fs.curdir, nil
}

// findVolumes finds the volume sectors and record its information.
func (fs *FileSystem) findVolumes() (err error) {
	numSectors := fs.r.NumSectors()

	var buf [maxSectorLength]byte
	rd := bytes.NewReader(buf[:])

	for sector := int64(16); sector < numSectors; sector++ {
		var vd volumeDescriptor

		_, err = fs.r.ReadSector(sector, buf[:])
		if err != nil {
			return
		}

		rd.Seek(0, os.SEEK_SET)
		err = binary.Read(rd, binary.LittleEndian, &vd)
		if err != nil {
			return
		}

		switch vd.Type {
		case 0: // boot record

		case 1, 2: // primary volume descriptor / supplementary volume descriptor
			p := &fs.pvd
			if vd.Type == 2 {
				p = &fs.svd
			}

			p.BlockSize = int64(binary.LittleEndian.Uint16(buf[128:]))
			p.Root, _ = readDir(buf[156:])
			p.PathTableSize = int64(binary.LittleEndian.Uint32(buf[132:]))
			p.PathTable[0] = int64(binary.LittleEndian.Uint32(buf[140:]))
			p.PathTable[1] = int64(binary.BigEndian.Uint32(buf[148:]))
			if p.BlockSize > int64(fs.r.SectorSize()) {
				return fmt.Errorf("invalid block size of %d bytes, cannot be bigger than sector size of %d bytes", p.BlockSize, fs.r.SectorSize())
			}

		case 255: // set terminator
			return nil
		}
	}

	return fmt.Errorf("could not find primary volume descriptor")
}

// buildPaths builds the paths from the path tables.
func (fs *FileSystem) buildPaths() {
	lba := int64(fs.pvd.PathTable[0])
	r := binary.ByteOrder(binary.LittleEndian)
	if lba == 0 {
		lba = int64(fs.pvd.PathTable[1])
		r = binary.BigEndian
	}

	b := make([]byte, maxSectorLength*2)
	s := 0
	e := 0
	for n := int64(0); n < fs.pvd.PathTableSize; {
		p, err := readPath(r, b[s:e])
		if err != nil {
			copy(b, b[s:e])

			nr, err := fs.r.ReadSector(lba, b[e-s:])
			if err != nil {
				return
			}
			if nr > int(fs.pvd.BlockSize) {
				nr = int(fs.pvd.BlockSize)
			}

			e = nr + e - s
			s = 0
			lba++
		} else {
			n += int64(p.Size)
			s += int(p.Size)
			if s > e {
				s = e
			}
			fs.paths = append(fs.paths, p)
		}
	}
}

// buildCache builds the cache of files by reading
// the path table if possible. Directories are not cached
// because the path table entries for them do not have enough
// metadata that the directory table entry provides.
// We will have to walk for the directories, but can lookup
// files immediately.
func (fs *FileSystem) buildCache() {
	fs.buildPaths()
	fs.dirs = make(map[string]bool)
	fs.files = make(map[string]File)

	b := make([]byte, maxSectorLength*2)
	for _, p := range fs.paths {
		_, err := fs.r.ReadSector(int64(p.LBA), b)
		if err != nil {
			continue
		}

		d := directory{
			LBA:   p.LBA,
			Nam:   p.Name,
			Flags: modeDir,
		}

		f := makeFile(fs, d)
		fi, err := f.Readdir(-1)
		if err != nil {
			continue
		}

		for _, fi := range fi {
			name := stdpath.Join(fs.fullPath(p), fi.Name())
			if fi.IsDir() {
				fs.dirs[name] = true
			} else {
				fs.files[name] = makeFile(fs, fi.(directory))
			}
		}
	}
}

// Open opens a file.
func (fs *FileSystem) Open(name string) (*File, error) {
	vd := &fs.pvd
	f := makeFile(fs, vd.Root)

	if name == "" {
		return nil, &os.PathError{"open", name, os.ErrNotExist}
	}

	xname := stdpath.Join(fs.curdir, strings.ToUpper(name))
	if f, exist := fs.files[xname]; exist {
		return &f, nil
	}

	toks := splitPath(xname)
loop:
	for i := len(toks) - 1; i >= 0; i-- {
		for {
			fi, err := f.Readdir(1024)
			if err == io.EOF {
				return nil, &os.PathError{"open", name, os.ErrNotExist}
			}
			if err != nil {
				return nil, err
			}

			for _, fi := range fi {
				if fi.Name() == toks[i] {
					f = makeFile(fs, fi.(directory))
					continue loop
				}
			}
		}
	}

	return &f, nil
}

// fullPath returns the full path of a path table entry by
// walking backwards from its indices.
func (fs *FileSystem) fullPath(p path) string {
	s := p.Name
	for {
		if !(0 <= p.Parent && int(p.Parent) < len(fs.paths)) {
			break
		}
		pp := p
		p = fs.paths[p.Parent]
		if p.Parent == pp.Parent {
			break
		}

		s = p.Name + "/" + s
	}
	return stdpath.Clean("/" + s)
}

// readPath reads one entry from the path table.
func readPath(r binary.ByteOrder, b []byte) (path, error) {
	if len(b) == 0 {
		return path{}, io.ErrUnexpectedEOF
	}

	size := 8 + uint16(b[0])
	if b[0]&1 != 0 {
		size++
	}
	if int(size) > len(b) {
		return path{}, io.ErrUnexpectedEOF
	}

	p := path{}
	p.Size = size
	p.ExSize = b[1]
	p.LBA = r.Uint32(b[2:])
	p.Parent = r.Uint16(b[6:])
	p.Name = string(b[8 : 8+b[0]])
	switch p.Name {
	case "\x00":
		p.Name = "."
	case "\x01":
		p.Name = ".."
	}
	p.Name = stdpath.Clean(p.Name)
	return p, nil
}

// readDir reads a directory entry from the ISO.
func readDir(p []byte) (directory, error) {
	switch {
	case len(p) < 34:
		fallthrough
	case len(p) < 34+int(p[32]):
		fallthrough
	case p[25]&modeDir != 0 && len(p) < int(p[0]):
		return directory{}, io.ErrUnexpectedEOF
	}

	r := binary.LittleEndian
	d := directory{}
	d.Siz = p[0]
	d.ExSize = p[1]
	d.LBA = r.Uint32(p[2:])
	d.Length = r.Uint32(p[10:])
	for i := range d.Time {
		d.Time[i] = p[18+i]
	}
	d.Flags = p[25]
	d.Interleave.Size = p[26]
	d.Interleave.Gap = p[27]
	d.Seq = r.Uint16(p[28:])
	d.Nam = string(p[33 : 33+p[32]])
	switch d.Nam {
	case "\x00":
		d.Nam = "."
	case "\x01":
		d.Nam = ".."
	}
	d.Nam = stdpath.Clean(d.Nam)

	return d, nil
}

func (p path) String() string {
	b := new(bytes.Buffer)
	fmt.Fprintf(b, "size: %v\n", p.Size)
	fmt.Fprintf(b, "lba: %v\n", p.LBA)
	fmt.Fprintf(b, "name: %q\n", p.Name)
	fmt.Fprintf(b, "parent: %v\n", p.Parent)
	return b.String()
}

func (d directory) ModTime() time.Time {
	p := d.Time[:]
	t := time.Date(int(p[0])+1900, time.Month(p[1]), int(p[2]), int(p[3]), int(p[4]), int(p[5]), 0, time.UTC)
	t.Add(time.Duration(int8(p[7])) * 15 * time.Minute)
	return t
}

func (d directory) Mode() os.FileMode {
	var mode os.FileMode
	if d.Flags&modeDir != 0 {
		mode |= os.ModeDir
	}
	return mode
}

func (d directory) IsDir() bool      { return d.Flags&modeDir != 0 }
func (d directory) Name() string     { return d.Nam }
func (d directory) Size() int64      { return int64(d.Length) }
func (d directory) Sys() interface{} { return d }

func (d directory) String() string {
	b := new(bytes.Buffer)
	fmt.Fprintf(b, "record size: %v\n", d.Siz)
	fmt.Fprintf(b, "extended record size: %v\n", d.ExSize)
	fmt.Fprintf(b, "lba: %v\n", d.LBA)
	fmt.Fprintf(b, "name: %q\n", d.Nam)
	fmt.Fprintf(b, "length: %v\n", d.Length)
	fmt.Fprintf(b, "flags: %#x\n", d.Flags)
	return b.String()
}

// File represents a directory entry inside an ISO.
type File struct {
	fs *FileSystem
	fi directory
	dp struct {
		buf        [maxSectorLength * 2]byte
		start, end int
		lba        int64
		eof        bool
	}
	off int64
}

// makeFile creates a file out of an iso directory entry.
func makeFile(fs *FileSystem, d directory) File {
	f := File{
		fs: fs,
		fi: d,
	}
	f.dp.lba = int64(d.LBA)
	return f
}

// Read reads data from the file into the buffer.
func (f *File) Read(p []byte) (n int, err error) {
	n, err = f.ReadAt(p, f.off)
	f.off += int64(n)
	return
}

// ReadAt reads the data from the file at an offset into the buffer.
func (f *File) ReadAt(p []byte, off int64) (n int, err error) {
	if f.fi.IsDir() {
		return 0, &os.PathError{"read", f.Name(), ErrIsDir}
	}

	if off >= int64(f.fi.Length) {
		return 0, io.EOF
	}

	if off < 0 {
		return 0, os.ErrInvalid
	}

	vd := &f.fs.pvd
	buf := make([]byte, maxSectorLength*2)
	r := f.fs.r
	lba := int64(f.fi.LBA) + off/vd.BlockSize
	s := int(off % vd.BlockSize)
	for {
		nr, err := r.ReadSector(lba, buf)
		if err != nil {
			break
		}

		e := nr
		if e > int(vd.BlockSize) {
			e = int(vd.BlockSize)
		}

		if int64(f.fi.Length)-(off+int64(n)) < int64(e-s) {
			e = s + int(int64(f.fi.Length)-(off+int64(n)))
		}

		b := buf[s:e]
		s = 0

		m := copy(p[n:], b)
		n += m
		if n >= len(p) || off+int64(m) >= int64(f.fi.Length) {
			break
		}

		lba++
	}
	return
}

// Seeks seeks the file to offset based on relative whence.
func (f *File) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case os.SEEK_SET:
	case os.SEEK_CUR:
		off += f.off
	case os.SEEK_END:
		off = int64(f.fi.Length) + off
	default:
		return 0, os.ErrInvalid
	}

	if off < 0 {
		return 0, os.ErrInvalid
	}

	f.off = off
	return off, nil
}

// resetDir resets the directory stream.
func (f *File) resetDir() {
	dp := &f.dp
	dp.eof = false
	dp.lba = int64(f.fi.LBA)
	dp.start = 0
	dp.end = 0
}

// Readdir reads a directory.
func (f *File) Readdir(n int) (fi []os.FileInfo, err error) {
	if !f.fi.IsDir() {
		return nil, &os.PathError{"readdir", f.fi.Name(), ErrNotDir}
	}

	vd := &f.fs.pvd
	dp := &f.dp
	if dp.eof {
		f.resetDir()
		return nil, io.EOF
	}

	b := dp.buf[:]
	s, e := dp.start, dp.end
	lba := dp.lba

	i := int64(0)
	for {
		if f.fi.Length != 0 && i >= int64(f.fi.Length) {
			break
		}

		d, xerr := readDir(b[s:e])
		if xerr != nil {
			copy(b, b[s:e])

			var nr int
			nr, err = f.fs.r.ReadSector(lba, b[e-s:])
			if err != nil {
				return
			}
			if nr > int(vd.BlockSize) {
				nr = int(vd.BlockSize)
			}

			e = nr + e - s
			s = 0
			lba++
		} else {
			if d.Siz == 0 {
				dp.eof = true
				if len(fi) == 0 {
					err = io.EOF
					defer f.resetDir()
				}
				break
			}
			i += int64(d.Siz)
			s += int(d.Siz)
			if s > e {
				s = e
			}

			fi = append(fi, d)
			if n > 0 && len(fi) >= n {
				break
			}
		}
	}

	dp.start, dp.end = s, e
	dp.lba = lba

	return
}

// Readdirnames reads a directory and returns up to n names
// in the directory. Use n <= 0 to get all the names.
func (f *File) Readdirnames(n int) (names []string, err error) {
	fi, err := f.Readdir(n)
	for _, fi := range fi {
		names = append(names, fi.Name())
	}
	return names, err
}

// Name returns the filename.
func (f *File) Name() string {
	return f.fi.Name()
}

// Stat returns the file information.
func (f *File) Stat() (fi os.FileInfo, err error) {
	return f.fi, nil
}

// Close closes the file.
func (f *File) Close() error {
	return nil
}

// splitPath splits a path into an array of tokens
// delimited by the path separator, but it returns it last to first element.
// An example is that "/test/foo" will return ["foo", "test"].
func splitPath(name string) []string {
	name = strings.ToUpper(stdpath.Clean(name))

	var toks []string
	for str := name; str != ""; {
		dir, base := stdpath.Split(str)
		if dir == "" && base == "" {
			break
		}

		if len(dir) > 0 && dir[len(dir)-1] == '/' {
			dir = dir[:len(dir)-1]
		}

		if base == "" {
			if dir == "" {
				dir = "."
			}
			toks = append(toks, dir)
			break
		}

		toks = append(toks, base)
		str = dir
	}
	return toks
}
