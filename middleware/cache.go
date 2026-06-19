package middleware

import (
	"context"
	"time"

	stdpath "path"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/honmaple/cloudfs"
)

type CacheOption struct {
	ExpireTime time.Duration `json:"expire_time"`
}

func (opt *CacheOption) NewFS(fs cloudfs.FS) (cloudfs.FS, error) {
	return newCacheFS(fs, opt)
}

type cacheFS struct {
	cloudfs.FS
	opt   *CacheOption
	cache *expirable.LRU[string, []cloudfs.FileInfo]
}

var _ cloudfs.FS = (*cacheFS)(nil)

func (d *cacheFS) List(ctx context.Context, path string, opts ...cloudfs.ListOption) ([]cloudfs.FileInfo, error) {
	files, ok := d.cache.Get(path)
	if ok {
		return files, nil
	}

	files, err := d.FS.List(ctx, path, opts...)
	if err != nil {
		return nil, err
	}

	d.cache.Add(path, files)
	return files, nil
}

// 部分服务会先获取文件信息，再获取列表，Get方式也需要缓存
func (d *cacheFS) Stat(ctx context.Context, path string) (cloudfs.FileInfo, error) {
	files, ok := d.cache.Get(stdpath.Dir(path))
	if ok {
		for _, file := range files {
			if file.Name() == stdpath.Base(path) {
				return file, nil
			}
		}
	}
	return d.FS.Stat(ctx, path)
}

func (d *cacheFS) Create(ctx context.Context, path string) (cloudfs.FileWriter, error) {
	w, err := d.FS.Create(ctx, path)
	if err != nil {
		return nil, err
	}
	d.cache.Remove(stdpath.Dir(path))
	return w, nil
}

func (d *cacheFS) Rename(ctx context.Context, path, newName string) error {
	if err := d.FS.Rename(ctx, path, newName); err != nil {
		return err
	}
	d.cache.Remove(stdpath.Dir(path))
	return nil
}

func (d *cacheFS) Move(ctx context.Context, src, dst string) error {
	if err := d.FS.Move(ctx, src, dst); err != nil {
		return err
	}
	d.cache.Remove(stdpath.Dir(src))
	d.cache.Remove(dst)
	return nil
}

func (d *cacheFS) Copy(ctx context.Context, src, dst string) error {
	if err := d.FS.Copy(ctx, src, dst); err != nil {
		return err
	}
	d.cache.Remove(dst)
	return nil
}

func (d *cacheFS) MakeDir(ctx context.Context, path string) error {
	if err := d.FS.MakeDir(ctx, path); err != nil {
		return err
	}
	d.cache.Remove(stdpath.Dir(path))
	return nil
}

func (d *cacheFS) Remove(ctx context.Context, path string) error {
	if err := d.FS.Remove(ctx, path); err != nil {
		return err
	}
	d.cache.Remove(stdpath.Dir(path))
	return nil
}

func newCacheFS(fs cloudfs.FS, opt *CacheOption) (cloudfs.FS, error) {
	if opt.ExpireTime <= 0 {
		opt.ExpireTime = 60
	}

	return &cacheFS{
		FS:    fs,
		opt:   opt,
		cache: expirable.NewLRU[string, []cloudfs.FileInfo](0, nil, opt.ExpireTime*time.Second),
	}, nil
}

func CacheFS(opt *CacheOption) cloudfs.WrapFunc {
	return opt.NewFS
}
