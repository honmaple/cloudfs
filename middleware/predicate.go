package middleware

import (
	"context"
	stdpath "path"
	"strings"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/utils/pathutil"
)

type PredicateOption struct {
	PathFn func(string) string
	FileFn func(cloudfs.FileInfo) (cloudfs.FileInfo, bool)
}

func (opt *PredicateOption) NewFS(fs cloudfs.FS) (cloudfs.FS, error) {
	return newPredicateFS(fs, opt), nil
}

type predicateFS struct {
	cloudfs.FS
	opt *PredicateOption
}

var _ cloudfs.FS = (*predicateFS)(nil)

func (d *predicateFS) getActualPath(path string) string {
	path = pathutil.CleanPath(path)
	if d.opt.PathFn == nil {
		return path
	}
	return d.opt.PathFn(path)
}

func (d *predicateFS) getActualFile(file cloudfs.FileInfo) (cloudfs.FileInfo, bool) {
	if d.opt.FileFn == nil {
		return file, true
	}
	return d.opt.FileFn(file)
}

func (d *predicateFS) List(ctx context.Context, path string, opts ...cloudfs.ListOption) ([]cloudfs.FileInfo, error) {
	files, err := d.FS.List(ctx, d.getActualPath(path), opts...)
	if err != nil {
		return nil, err
	}
	if d.opt.FileFn == nil {
		return files, nil
	}

	newFiles := make([]cloudfs.FileInfo, 0, len(files))
	for _, file := range files {
		newFile, ok := d.getActualFile(file)
		if !ok {
			continue
		}
		newFiles = append(newFiles, newFile)
	}
	return newFiles, nil
}

func (d *predicateFS) Stat(ctx context.Context, path string) (cloudfs.FileInfo, error) {
	file, err := d.FS.Stat(ctx, d.getActualPath(path))
	if err != nil {
		return nil, err
	}

	newFile, _ := d.getActualFile(file)
	return newFile, nil
}

func (d *predicateFS) Open(ctx context.Context, path string) (cloudfs.File, error) {
	return d.FS.Open(ctx, d.getActualPath(path))
}

func (d *predicateFS) Create(ctx context.Context, path string) (cloudfs.FileWriter, error) {
	return d.FS.Create(ctx, d.getActualPath(path))
}

func (d *predicateFS) Copy(ctx context.Context, src string, dst string) error {
	return d.FS.Copy(ctx, d.getActualPath(src), d.getActualPath(dst))
}

func (d *predicateFS) Move(ctx context.Context, src string, dst string) error {
	return d.FS.Move(ctx, d.getActualPath(src), d.getActualPath(dst))
}

func (d *predicateFS) Rename(ctx context.Context, path, newName string) error {
	return d.FS.Rename(ctx, d.getActualPath(path), newName)
}

func (d *predicateFS) Remove(ctx context.Context, path string) error {
	return d.FS.Remove(ctx, d.getActualPath(path))
}

func (d *predicateFS) MakeDir(ctx context.Context, path string) error {
	return d.FS.MakeDir(ctx, d.getActualPath(path))
}

func newPredicateFS(fs cloudfs.FS, opt *PredicateOption) cloudfs.FS {
	return &predicateFS{FS: fs, opt: opt}
}

func PredicateFS(opt *PredicateOption) cloudfs.WrapFunc {
	return opt.NewFS
}

func PrefixFS(prefix string) cloudfs.WrapFunc {
	return func(fs cloudfs.FS) (cloudfs.FS, error) {
		if prefix == "" {
			return fs, nil
		}
		opt := &PredicateOption{
			PathFn: func(path string) string {
				return stdpath.Join(prefix, path)
			},
			FileFn: func(file cloudfs.FileInfo) (cloudfs.FileInfo, bool) {
				return cloudfs.NewFileInfo(file, func(info *cloudfs.Entry) { info.Path = strings.TrimPrefix(file.Path(), prefix) }), true
			},
		}
		return newPredicateFS(fs, opt), nil
	}
}

func TrimPrefixFS(fs cloudfs.FS, prefix string) cloudfs.WrapFunc {
	return func(fs cloudfs.FS) (cloudfs.FS, error) {
		if prefix == "" {
			return fs, nil
		}
		opt := &PredicateOption{
			PathFn: func(path string) string {
				return strings.TrimPrefix(path, prefix)
			},
			FileFn: func(file cloudfs.FileInfo) (cloudfs.FileInfo, bool) {
				return cloudfs.NewFileInfo(file, func(info *cloudfs.Entry) { info.Path = stdpath.Join(prefix, file.Path()) }), true
			},
		}
		return newPredicateFS(fs, opt), nil
	}
}
