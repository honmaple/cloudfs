package quark

import (
	"context"
	"time"

	stdpath "path"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/honmaple/cloudfs"
	"github.com/spf13/viper"
)

type wrapFS struct {
	cloudfs.FS
	cache *expirable.LRU[string, cloudfs.FileInfo]
}

var _ cloudfs.FS = (*wrapFS)(nil)

func (d *wrapFS) getActualPath(ctx context.Context, path string) (string, error) {
	if path == "/" {
		return "0", nil
	}

	file, err := d.Stat(ctx, path)
	if err != nil {
		return "", err
	}
	cf := viper.New()
	for k, v := range file.ExtraInfo() {
		cf.Set(k, v)
	}
	return cf.GetString("id"), nil
}

func (d *wrapFS) List(ctx context.Context, path string) ([]cloudfs.FileInfo, error) {
	path, query := cloudfs.ParsePath(path)
	actualPath, err := d.getActualPath(ctx, path)
	if err != nil {
		return nil, err
	}
	files, err := d.FS.List(ctx, cloudfs.PathWithQuery(actualPath, query))
	if err != nil {
		return nil, err
	}

	newFiles := make([]cloudfs.FileInfo, len(files))
	for i, file := range files {
		newFiles[i] = cloudfs.NewFileInfo(file,
			func(info *cloudfs.Entry) {
				info.Path = path
				info.ExtraInfo = file.ExtraInfo()
			},
		)
	}
	return newFiles, nil
}

func (d *wrapFS) Rename(ctx context.Context, path, newName string) error {
	actualPath, err := d.getActualPath(ctx, path)
	if err != nil {
		return err
	}
	return d.FS.Rename(ctx, actualPath, newName)
}

func (d *wrapFS) Move(ctx context.Context, src, dst string) error {
	actualSrcPath, err := d.getActualPath(ctx, src)
	if err != nil {
		return err
	}
	actualDstPath, err := d.getActualPath(ctx, dst)
	if err != nil {
		return err
	}
	return d.FS.Move(ctx, actualSrcPath, actualDstPath)
}

func (d *wrapFS) Copy(ctx context.Context, src, dst string) error {
	actualSrcPath, err := d.getActualPath(ctx, src)
	if err != nil {
		return err
	}
	actualDstPath, err := d.getActualPath(ctx, src)
	if err != nil {
		return err
	}
	return d.FS.Copy(ctx, actualSrcPath, actualDstPath)
}

func (d *wrapFS) MakeDir(ctx context.Context, path string) error {
	actualPath, err := d.getActualPath(ctx, stdpath.Dir(path))
	if err != nil {
		return err
	}
	return d.FS.MakeDir(ctx, stdpath.Join(actualPath, stdpath.Base(path)))
}

func (d *wrapFS) Remove(ctx context.Context, path string) error {
	actualPath, err := d.getActualPath(ctx, path)
	if err != nil {
		return err
	}
	return d.FS.Remove(ctx, actualPath)
}

func (d *wrapFS) Open(ctx context.Context, path string) (cloudfs.File, error) {
	actualPath, err := d.getActualPath(ctx, path)
	if err != nil {
		return nil, err
	}
	return d.FS.Open(ctx, actualPath)
}

func (d *wrapFS) Stat(ctx context.Context, path string) (cloudfs.FileInfo, error) {
	// /aaa/bbb/ccc/ddd
	if path == "/" {
		return nil, cloudfs.ErrNotSupport
	}

	if file, ok := d.cache.Get(path); ok {
		return file, nil
	}

	dir, name := stdpath.Split(path)

	files, err := d.List(ctx, stdpath.Clean(dir))
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		if file.Name() == name {
			d.cache.Add(path, file)
			return file, nil
		}
	}
	return nil, cloudfs.ErrDstNotExist
}

func WrapFS(fs cloudfs.FS, expireTime time.Duration) cloudfs.FS {
	if expireTime <= 0 {
		expireTime = 60
	}
	return &wrapFS{
		FS:    fs,
		cache: expirable.NewLRU[string, cloudfs.FileInfo](0, nil, expireTime*time.Second),
	}
}
