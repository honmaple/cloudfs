package smb

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"

	stdpath "path"

	"github.com/hirochachacha/go-smb2"
	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/driver"
	"github.com/honmaple/cloudfs/middleware"
	"github.com/honmaple/cloudfs/utils/ioutil"
	"github.com/honmaple/cloudfs/utils/structutil"
)

type Option struct {
	Host      string `json:"host"       validate:"required"`
	Port      int    `json:"port"`
	Username  string `json:"username"   validate:"required"`
	Password  string `json:"password"   validate:"required"`
	Domain    string `json:"domain"`
	ShareName string `json:"share_name" validate:"required"`
}

func (opt *Option) NewFS() (cloudfs.FS, error) {
	return New(opt)
}

type SMB struct {
	cloudfs.BaseFS
	opt    *Option
	client *smb2.Share
}

var _ cloudfs.FS = (*SMB)(nil)

func (d *SMB) Close() error {
	return d.client.Umount()
}

func (d *SMB) Stat(ctx context.Context, path string) (cloudfs.FileInfo, error) {
	info, err := d.client.Stat(path)
	if err != nil {
		return nil, err
	}
	return cloudfs.NewFileInfo(info, func(info *cloudfs.Entry) { info.Path = path }), nil
}

func (d *SMB) Open(ctx context.Context, path string) (cloudfs.File, error) {
	return d.client.Open(path)
}

func (d *SMB) Create(ctx context.Context, path string) (cloudfs.FileWriter, error) {
	return d.client.Create(path)
}

func (d *SMB) List(ctx context.Context, path string, opts ...cloudfs.ListOption) ([]cloudfs.FileInfo, error) {
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

func (d *SMB) Move(ctx context.Context, src, dst string) error {
	return d.client.Rename(src, dst)
}

func (d *SMB) copyFile(ctx context.Context, src, dst string) error {
	srcFile, err := d.client.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := d.client.Open(dst)
	if err != nil {
		return err
	}

	if _, err = ioutil.CopyWithContext(ctx, dstFile, srcFile); err != nil {
		dstFile.Close()
		return err
	}

	if err = dstFile.Close(); err != nil {
		return err
	}

	info, err := d.client.Stat(src)
	if err != nil {
		return err
	}
	return d.client.Chmod(dst, info.Mode())
}

func (d *SMB) copyDir(ctx context.Context, src, dst string) error {
	info, err := d.client.Stat(src)
	if err != nil {
		return err
	}

	if err := d.client.MkdirAll(dst, info.Mode()); err != nil {
		return err
	}

	files, err := d.client.ReadDir(src)
	if err != nil {
		return fmt.Errorf("cannot read dir %s: %s", src, err.Error())
	}

	for _, file := range files {
		srcPath := stdpath.Join(src, file.Name())
		dstPath := stdpath.Join(dst, file.Name())

		if file.IsDir() {
			err = d.copyDir(ctx, srcPath, dstPath)
		} else {
			err = d.copyFile(ctx, srcPath, dstPath)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *SMB) Copy(ctx context.Context, src, dst string) error {
	dstFile, err := d.Stat(ctx, dst)
	if err != nil {
		return err
	} else if !dstFile.IsDir() {
		return &fs.PathError{Op: "copy", Path: dst, Err: errors.New("copy dst must be a dir")}
	} else {
		dst = stdpath.Join(dst, stdpath.Base(src))
	}

	srcFile, err := d.Stat(ctx, src)
	if err != nil {
		return err
	}
	if srcFile.IsDir() {
		return d.copyDir(ctx, src, dst)
	}
	return d.copyFile(ctx, src, dst)
}

func (d *SMB) Rename(ctx context.Context, path, newName string) error {
	return d.client.Rename(path, stdpath.Join(stdpath.Dir(path), newName))
}

func (d *SMB) MakeDir(ctx context.Context, path string) error {
	return d.client.Mkdir(path, 0700)
}

func (d *SMB) removeFile(path string) error {
	return d.client.Remove(path)
}

func (d *SMB) removeDir(path string) error {
	return d.client.RemoveAll(path)
}

func (d *SMB) Remove(ctx context.Context, path string) error {
	fi, err := d.client.Stat(path)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return d.removeDir(path)
	}
	return d.removeFile(path)
}

func New(opt *Option) (cloudfs.FS, error) {
	if err := structutil.Verify(opt); err != nil {
		return nil, err
	}

	if opt.Port == 0 {
		opt.Port = 445
	}

	conn, err := net.Dial("tcp", fmt.Sprintf("%s:%d", opt.Host, opt.Port))
	if err != nil {
		return nil, err
	}

	dialer := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     opt.Username,
			Password: opt.Password,
			Domain:   opt.Domain,
		},
	}

	s, err := dialer.Dial(conn)
	if err != nil {
		return nil, err
	}

	client, err := s.Mount(opt.ShareName)
	if err != nil {
		return nil, err
	}

	d := &SMB{opt: opt, client: client}
	// smb访问路径不能以/开头
	return middleware.NewFS(d, middleware.TrimPrefixFS(d, "/"))
}

func init() {
	driver.Register("smb", func() driver.Option {
		return &Option{}
	})
}
