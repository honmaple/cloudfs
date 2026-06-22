package webdav

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"

	stdpath "path"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/driver"
	"github.com/honmaple/cloudfs/utils/ioutil"
	"github.com/honmaple/cloudfs/utils/structutil"
	"github.com/studio-b12/gowebdav"
)

type Option struct {
	Endpoint string      `json:"endpoint"  validate:"required"`
	Username string      `json:"username"  validate:"required"`
	Password string      `json:"password"  validate:"required"`
	DirPerm  os.FileMode `json:"dir_perm"`
}

func (opt *Option) NewFS() (cloudfs.FS, error) {
	return New(opt)
}

type Webdav struct {
	cloudfs.BaseFS
	opt    *Option
	client *gowebdav.Client
}

var _ cloudfs.FS = (*Webdav)(nil)

func (d *Webdav) List(ctx context.Context, path string, opts ...cloudfs.ListOption) ([]cloudfs.FileInfo, error) {
	infos, err := d.client.ReadDir(path)
	if err != nil {
		return nil, err
	}

	files := make([]cloudfs.FileInfo, len(infos))
	for i, info := range infos {
		files[i] = cloudfs.NewFileInfo(info, func(info *cloudfs.Entry) { info.Path = path })
	}
	return files, nil
}

func (d *Webdav) Move(ctx context.Context, src, dst string) error {
	dstFile, err := d.Stat(ctx, dst)
	if err != nil {
		return err
	} else if !dstFile.IsDir() {
		return &fs.PathError{Op: "move", Path: dst, Err: errors.New("move dst must be a dir")}
	} else {
		dst = stdpath.Join(dst, stdpath.Base(src))
	}
	return d.client.Rename(src, dst, false)
}

func (d *Webdav) Copy(ctx context.Context, src, dst string) error {
	dstFile, err := d.Stat(ctx, dst)
	if err != nil {
		return err
	} else if !dstFile.IsDir() {
		return &fs.PathError{Op: "copy", Path: dst, Err: errors.New("copy dst must be a dir")}
	} else {
		dst = stdpath.Join(dst, stdpath.Base(src))
	}
	return d.client.Copy(src, dst, false)
}

func (d *Webdav) Rename(ctx context.Context, path, newName string) error {
	return d.client.Rename(path, stdpath.Join(stdpath.Dir(path), newName), false)
}

func (d *Webdav) Remove(ctx context.Context, path string) error {
	return d.client.Remove(path)
}

func (d *Webdav) MakeDir(ctx context.Context, path string) error {
	return d.client.MkdirAll(path, d.opt.DirPerm)
}

func (d *Webdav) Open(ctx context.Context, path string) (cloudfs.File, error) {
	info, err := d.client.Stat(path)
	if err != nil {
		return nil, err
	}

	rangeFunc := func(offset, length int64) (io.ReadCloser, error) {
		return d.client.ReadStreamRange(path, offset, length)
	}
	return cloudfs.NewFile(info.Size(), rangeFunc)

}

func (d *Webdav) Create(ctx context.Context, path string) (cloudfs.FileWriter, error) {
	r, w := ioutil.Pipe()
	go func() {
		r.CloseWithError(d.client.WriteStream(path, r, d.opt.DirPerm))
	}()
	return w, nil
}

func (d *Webdav) Stat(ctx context.Context, path string) (cloudfs.FileInfo, error) {
	fi, err := d.client.Stat(path)
	if err != nil {
		rawErr := cloudfs.UnderlyingError(err)
		if s, ok := rawErr.(gowebdav.StatusError); ok {
			switch s.Status {
			case 403:
				return nil, os.ErrPermission
			case 404:
				return nil, os.ErrNotExist
			}
		}
		return nil, err
	}
	// 绿联webdav stat无法获取文件名
	return cloudfs.NewFileInfo(fi, func(info *cloudfs.Entry) {
		info.Path = stdpath.Dir(path)
		info.Name = stdpath.Base(path)
	}), nil
}

func (d *Webdav) Close() error {
	return nil
}

func New(opt *Option) (cloudfs.FS, error) {
	if err := structutil.Verify(opt); err != nil {
		return nil, err
	}
	opt.DirPerm = 0755

	client := gowebdav.NewAuthClient(opt.Endpoint, gowebdav.NewAutoAuth(opt.Username, opt.Password))
	if err := client.Connect(); err != nil {
		return nil, err
	}
	d := &Webdav{opt: opt, client: client}
	return d, nil
}

func init() {
	driver.Register("webdav", func() driver.Option {
		return &Option{}
	})
}
