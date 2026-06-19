package ftp

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"time"

	stdpath "path"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/driver"
	"github.com/honmaple/cloudfs/utils/ioutil"
	"github.com/honmaple/cloudfs/utils/structutil"
	"github.com/jlaffaye/ftp"
)

type Option struct {
	Host     string `json:"host"      validate:"required"`
	Port     int    `json:"port"`
	Username string `json:"username"  validate:"required"`
	Password string `json:"password"  validate:"required"`
}

func (opt *Option) NewFS() (cloudfs.FS, error) {
	return New(opt)
}

type FTP struct {
	cloudfs.BaseFS
	opt    *Option
	client *ftp.ServerConn
}

var _ cloudfs.FS = (*FTP)(nil)

func (d *FTP) Close() error {
	return d.client.Logout()
}

func (d *FTP) Stat(ctx context.Context, path string) (cloudfs.FileInfo, error) {
	info, err := d.client.GetEntry(path)
	if err != nil {
		return nil, err
	}
	return cloudfs.NewFileInfo(&fileinfo{info}, func(info *cloudfs.Entry) { info.Path = stdpath.Dir(path) }), nil
}

func (d *FTP) Open(ctx context.Context, path string) (cloudfs.File, error) {
	info, err := d.client.GetEntry(path)
	if err != nil {
		return nil, err
	}
	if info.Type == ftp.EntryTypeFolder {
		return nil, &fs.PathError{Op: "open", Path: path, Err: cloudfs.ErrOpenDirectory}
	}

	rangeFunc := func(offset, length int64) (io.ReadCloser, error) {
		return d.client.RetrFrom(path, uint64(offset))
	}
	return cloudfs.NewFile(int64(info.Size), rangeFunc)
}

func (d *FTP) Create(ctx context.Context, path string) (cloudfs.FileWriter, error) {
	r, w := ioutil.Pipe()
	go func() {
		err := d.client.Stor(path, r)
		r.CloseWithError(err)
	}()
	return w, nil
}

func (d *FTP) List(ctx context.Context, path string, opts ...cloudfs.ListOption) ([]cloudfs.FileInfo, error) {
	entries, err := d.client.List(path)
	if err != nil {
		return nil, err
	}

	files := make([]cloudfs.FileInfo, len(entries))
	for i, info := range entries {
		files[i] = cloudfs.NewFileInfo(&fileinfo{info}, func(info *cloudfs.Entry) { info.Path = path })
	}
	return files, nil
}

func (d *FTP) Move(ctx context.Context, src, dst string) error {
	return d.client.Rename(src, dst)
}

func (d *FTP) Copy(ctx context.Context, src, dst string) error {
	return cloudfs.Copy(ctx, d, src, dst)
}

func (d *FTP) Rename(ctx context.Context, path, newName string) error {
	return d.client.Rename(path, stdpath.Join(stdpath.Dir(path), newName))
}

func (d *FTP) MakeDir(ctx context.Context, path string) error {
	return d.client.MakeDir(path)
}

func (d *FTP) removeFile(path string) error {
	return d.client.Delete(path)
}

func (d *FTP) removeDir(path string) error {
	return d.client.RemoveDirRecur(path)
}

func (d *FTP) Remove(ctx context.Context, path string) error {
	fi, err := d.client.GetEntry(path)
	if err != nil {
		return err
	}
	if fi.Type == ftp.EntryTypeFolder {
		return d.removeDir(path)
	}
	return d.removeFile(path)
}

func New(opt *Option) (cloudfs.FS, error) {
	if err := structutil.Verify(opt); err != nil {
		return nil, err
	}

	if opt.Port == 0 {
		opt.Port = 21
	}

	conn, err := ftp.Dial(fmt.Sprintf("%s:%d", opt.Host, opt.Port), ftp.DialWithTimeout(10*time.Second))
	if err != nil {
		return nil, err
	}

	if err := conn.Login(opt.Username, opt.Password); err != nil {
		return nil, err
	}

	d := &FTP{opt: opt, client: conn}
	return d, nil
}

func init() {
	driver.Register("ftp", func() driver.Option {
		return &Option{}
	})
}
