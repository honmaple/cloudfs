package upyun

import (
	"context"
	"fmt"
	"io"

	stdpath "path"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/driver"
	"github.com/honmaple/cloudfs/utils/ioutil"
	"github.com/honmaple/cloudfs/utils/structutil"
	"github.com/upyun/go-sdk/v3/upyun"
)

type Option struct {
	Bucket   string `json:"bucket"    validate:"required"`
	Operator string `json:"operator"  validate:"required"`
	Password string `json:"password"  validate:"required"`
}

func (opt *Option) NewFS() (cloudfs.FS, error) {
	return New(opt)
}

type Upyun struct {
	cloudfs.BaseFS
	opt    *Option
	client *upyun.UpYun
}

var _ cloudfs.FS = (*Upyun)(nil)

func newFileInfo(path string, info *upyun.FileInfo) cloudfs.FileInfo {
	entry := &cloudfs.Entry{
		Path:    path,
		Name:    stdpath.Base(info.Name),
		Size:    info.Size,
		IsDir:   info.IsDir,
		ModTime: info.Time,
	}
	return entry.FileInfo()
}

func (d *Upyun) List(ctx context.Context, path string, opts ...cloudfs.ListOption) ([]cloudfs.FileInfo, error) {
	errs := make(chan error, 1)
	defer close(errs)

	infos := make(chan *upyun.FileInfo)
	go func() {
		errs <- d.client.List(&upyun.GetObjectsConfig{
			Path:        path,
			ObjectsChan: infos,
		})
	}()

	files := make([]cloudfs.FileInfo, 0)
	for info := range infos {
		files = append(files, newFileInfo(path, info))
	}

	if err := <-errs; err != nil {
		return nil, err
	}
	return files, nil
}

func (d *Upyun) Rename(ctx context.Context, path, newName string) error {
	return d.client.Move(&upyun.MoveObjectConfig{
		SrcPath:  path,
		DestPath: stdpath.Join(stdpath.Dir(path), newName),
	})
}

func (d *Upyun) Move(ctx context.Context, src, dst string) error {
	return d.client.Move(&upyun.MoveObjectConfig{
		SrcPath:  src,
		DestPath: stdpath.Join(dst, stdpath.Base(src)),
	})
}

func (d *Upyun) Copy(ctx context.Context, src, dst string) error {
	return d.client.Copy(&upyun.CopyObjectConfig{
		SrcPath:  src,
		DestPath: stdpath.Join(dst, stdpath.Base(src)),
	})
}

func (d *Upyun) Remove(ctx context.Context, path string) error {
	return d.client.Delete(&upyun.DeleteObjectConfig{
		Path:  path,
		Async: false,
	})
}

func (d *Upyun) MakeDir(ctx context.Context, path string) error {
	return d.client.Mkdir(path)
}

func (d *Upyun) Stat(ctx context.Context, path string) (cloudfs.FileInfo, error) {
	info, err := d.client.GetInfo(path)
	if err != nil {
		return nil, err
	}
	return newFileInfo(stdpath.Dir(path), info), nil
}

func (d *Upyun) Open(ctx context.Context, path string) (cloudfs.File, error) {
	info, err := d.client.GetInfo(path)
	if err != nil {
		return nil, err
	}

	rangeFunc := func(offset, length int64) (io.ReadCloser, error) {
		headers := make(map[string]string)
		if length > 0 {
			headers["Range"] = fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)
		} else {
			headers["Range"] = fmt.Sprintf("bytes=%d-", offset)
		}

		r, w := ioutil.Pipe()
		go func() {
			_, err := d.client.Get(&upyun.GetObjectConfig{
				Path:    path,
				Writer:  w,
				Headers: headers,
			})
			w.CloseWithError(err)
		}()
		return r, nil
	}
	return cloudfs.NewFile(info.Size, rangeFunc)
}

func (d *Upyun) Create(ctx context.Context, path string) (cloudfs.FileWriter, error) {
	r, w := ioutil.Pipe()
	go func() {
		err := d.client.Put(&upyun.PutObjectConfig{
			Path:   path,
			Reader: r,
		})
		r.CloseWithError(err)
	}()
	return w, nil
}

func (d *Upyun) Close() error {
	// 不要执行d.client.Close(), 会导致 panic: close of nil channel
	return nil
}

func New(opt *Option) (cloudfs.FS, error) {
	if err := structutil.Verify(opt); err != nil {
		return nil, err
	}

	d := &Upyun{
		opt: opt,
		client: upyun.NewUpYun(&upyun.UpYunConfig{
			Bucket:   opt.Bucket,
			Operator: opt.Operator,
			Password: opt.Password,
		}),
	}
	return d, nil
}

func init() {
	driver.Register("upyun", func() driver.Option {
		return &Option{}
	})
}
