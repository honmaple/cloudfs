package cloudfs

import (
	"io"
	"io/fs"
	"mime"
	"os"
	stdpath "path"
	"time"
)

type (
	File interface {
		io.Seeker
		io.ReadCloser
	}
	FileWriter interface {
		io.WriteCloser
	}
	FileInfo interface {
		fs.FileInfo
		Type() string
		Path() string
		ExtraInfo() map[string]any
	}
)

type seeker struct {
	r            io.ReadCloser
	offset       int64
	readAtOffset int64
	size         int64
	rangeFunc    func(int64, int64) (io.ReadCloser, error)
}

func (s *seeker) Read(buf []byte) (n int, err error) {
	n, err = s.ReadAt(buf, s.offset)
	s.offset += int64(n)
	return
}

func (s *seeker) ReadAt(buf []byte, off int64) (n int, err error) {
	if off < 0 {
		return -1, os.ErrInvalid
	}

	if off != s.readAtOffset && s.r != nil {
		_ = s.r.Close()
		s.r = nil
	}

	if s.r == nil {
		s.r, err = s.rangeFunc(int64(off), 0)
		s.readAtOffset = off
		if err != nil {
			return 0, err
		}
	}

	n, err = s.r.Read(buf)
	s.readAtOffset += int64(n)
	return
}

func (s *seeker) Seek(offset int64, whence int) (int64, error) {
	oldOffset := s.offset
	var newOffset int64
	switch whence {
	case io.SeekStart:
		newOffset = offset
	case io.SeekCurrent:
		newOffset = oldOffset + offset
	case io.SeekEnd:
		return s.size, nil
	default:
		return -1, os.ErrInvalid
	}

	if newOffset < 0 {
		return oldOffset, os.ErrInvalid
	}
	if newOffset == oldOffset {
		return oldOffset, nil
	}
	s.offset = newOffset
	return newOffset, nil
}

func (s *seeker) Close() error {
	if s.r != nil {
		return s.r.Close()
	}
	return nil
}

type Entry struct {
	Name      string
	Type      string
	Size      int64
	Path      string
	Mode      fs.FileMode
	IsDir     bool
	ModTime   time.Time
	ExtraInfo map[string]any
	Sys       any
}

func (f *Entry) FileInfo() FileInfo {
	if f.Path == "" || f.Path == "." {
		f.Path = "/"
	}
	return &fileInfo{
		name:      f.Name,
		typ:       f.Type,
		size:      f.Size,
		path:      f.Path,
		mode:      f.Mode,
		isDir:     f.IsDir,
		modTime:   f.ModTime,
		extraInfo: f.ExtraInfo,
		sys:       f.Sys,
	}
}

type fileInfo struct {
	name      string
	typ       string
	size      int64
	path      string
	mode      fs.FileMode
	isDir     bool
	modTime   time.Time
	extraInfo map[string]any
	sys       any
}

func (f *fileInfo) Type() string {
	if f.typ != "" {
		return f.typ
	}
	if f.isDir {
		return "DIR"
	}
	return mime.TypeByExtension(stdpath.Ext(f.name))
}
func (f *fileInfo) Path() string              { return f.path }
func (f *fileInfo) Name() string              { return f.name }
func (f *fileInfo) Size() int64               { return f.size }
func (f *fileInfo) Mode() fs.FileMode         { return f.mode }
func (f *fileInfo) IsDir() bool               { return f.isDir }
func (f *fileInfo) ModTime() time.Time        { return f.modTime }
func (f *fileInfo) Sys() any                  { return f.sys }
func (f *fileInfo) ExtraInfo() map[string]any { return f.extraInfo }

func NewFileInfo(info fs.FileInfo, opts ...func(*Entry)) FileInfo {
	fi := &Entry{
		Name:    info.Name(),
		Size:    info.Size(),
		Mode:    info.Mode(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
		Sys:     info.Sys(),
	}
	for _, opt := range opts {
		opt(fi)
	}
	return fi.FileInfo()
}

func NewFile(size int64, rangeFunc func(int64, int64) (io.ReadCloser, error)) (File, error) {
	return &seeker{
		size:      size,
		rangeFunc: rangeFunc,
	}, nil
}
