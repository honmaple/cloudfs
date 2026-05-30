package middleware

import (
	"compress/gzip"
	"io"

	"github.com/honmaple/cloudfs"
)

type WrapReader struct {
	cloudfs.FileReader
	r io.Reader
}

func (r *WrapReader) Read(p []byte) (n int, err error) { return r.r.Read(p) }

type WrapWriter struct {
	cloudfs.FileWriter
	w io.WriteCloser
}

func (w *WrapWriter) Write(p []byte) (n int, err error) { return w.w.Write(p) }
func (w *WrapWriter) Close() error {
	w.FileWriter.Close()
	w.w.Close()
	return nil
}

type CompressOption struct {
	Level int `json:"level"`
}

func (opt *CompressOption) NewFS(fs cloudfs.FS) (cloudfs.FS, error) {
	return newCompressFS(fs, opt)
}

type compressFS struct {
	cloudfs.FS
	opt *CompressOption
}

var _ cloudfs.FS = (*compressFS)(nil)

func (d *compressFS) compress(out io.Writer) (*gzip.Writer, error) {
	level := d.opt.Level
	if level == 0 {
		level = gzip.BestCompression
	}
	return gzip.NewWriterLevel(out, level)
}

func (d *compressFS) uncompress(in io.Reader) (*gzip.Reader, error) {
	return gzip.NewReader(in)
}

func (d *compressFS) Open(path string) (cloudfs.FileReader, error) {
	r, err := d.FS.Open(path)
	if err != nil {
		return nil, err
	}

	nr, err := d.uncompress(r)
	if err != nil {
		return nil, err
	}
	return &WrapReader{r, nr}, nil
}

func (d *compressFS) Create(path string) (cloudfs.FileWriter, error) {
	w, err := d.FS.Create(path)
	if err != nil {
		return nil, err
	}
	nw, err := d.compress(w)
	if err != nil {
		return nil, err
	}
	return &WrapWriter{w, nw}, nil
}

func newCompressFS(fs cloudfs.FS, opt *CompressOption) (cloudfs.FS, error) {
	return &compressFS{FS: fs, opt: opt}, nil
}

func CompressFS(opt *CompressOption) cloudfs.WrapFunc {
	return opt.NewFS
}
