package middleware

import (
	"context"
	filepath "path"
	"strings"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/utils/pathutil"
)

type HookOption struct {
	PathFn func(string) string
	FileFn func(cloudfs.File) (cloudfs.File, bool)
}

func (opt *HookOption) NewFS(fs cloudfs.FS) (cloudfs.FS, error) {
	return newHookFS(fs, opt), nil
}

type hookFS struct {
	cloudfs.FS
	opt *HookOption
}

var _ cloudfs.FS = (*hookFS)(nil)

func (d *hookFS) getActualPath(path string) string {
	path = pathutil.CleanPath(path)
	if d.opt.PathFn == nil {
		return path
	}
	return d.opt.PathFn(path)
}

func (d *hookFS) getActualFile(file cloudfs.File) (cloudfs.File, bool) {
	if d.opt.FileFn == nil {
		return file, true
	}
	return d.opt.FileFn(file)
}

func (d *hookFS) List(ctx context.Context, path string, opts ...cloudfs.ListOption) ([]cloudfs.File, error) {
	files, err := d.FS.List(ctx, d.getActualPath(path), opts...)
	if err != nil {
		return nil, err
	}
	if d.opt.FileFn == nil {
		return files, nil
	}

	newFiles := make([]cloudfs.File, 0, len(files))
	for _, file := range files {
		newFile, ok := d.getActualFile(file)
		if !ok {
			continue
		}
		newFiles = append(newFiles, newFile)
	}
	return newFiles, nil
}

func (d *hookFS) Get(ctx context.Context, path string) (cloudfs.File, error) {
	file, err := d.FS.Get(ctx, d.getActualPath(path))
	if err != nil {
		return nil, err
	}

	newFile, _ := d.getActualFile(file)
	return newFile, nil
}

func (d *hookFS) Open(path string) (cloudfs.FileReader, error) {
	return d.FS.Open(d.getActualPath(path))
}

func (d *hookFS) Create(path string) (cloudfs.FileWriter, error) {
	return d.FS.Create(d.getActualPath(path))
}

func (d *hookFS) Copy(ctx context.Context, src string, dst string) error {
	return d.FS.Copy(ctx, d.getActualPath(src), d.getActualPath(dst))
}

func (d *hookFS) Move(ctx context.Context, src string, dst string) error {
	return d.FS.Move(ctx, d.getActualPath(src), d.getActualPath(dst))
}

func (d *hookFS) Rename(ctx context.Context, path, newName string) error {
	return d.FS.Rename(ctx, d.getActualPath(path), newName)
}

func (d *hookFS) Remove(ctx context.Context, path string) error {
	return d.FS.Remove(ctx, d.getActualPath(path))
}

func (d *hookFS) MakeDir(ctx context.Context, path string) error {
	return d.FS.MakeDir(ctx, d.getActualPath(path))
}

func newHookFS(fs cloudfs.FS, opt *HookOption) cloudfs.FS {
	return &hookFS{FS: fs, opt: opt}
}

func HookFS(opt *HookOption) cloudfs.WrapFunc {
	return opt.NewFS
}

func PrefixFS(prefix string) cloudfs.WrapFunc {
	return func(fs cloudfs.FS) (cloudfs.FS, error) {
		if prefix == "" {
			return fs, nil
		}
		opt := &HookOption{
			PathFn: func(path string) string {
				return filepath.Join(prefix, path)
			},
			FileFn: func(file cloudfs.File) (cloudfs.File, bool) {
				return cloudfs.NewFile(strings.TrimPrefix(file.Path(), prefix), file), true
			},
		}
		return newHookFS(fs, opt), nil
	}
}

func TrimPrefixFS(fs cloudfs.FS, prefix string) cloudfs.WrapFunc {
	return func(fs cloudfs.FS) (cloudfs.FS, error) {
		if prefix == "" {
			return fs, nil
		}
		opt := &HookOption{
			PathFn: func(path string) string {
				return strings.TrimPrefix(path, prefix)
			},
			FileFn: func(file cloudfs.File) (cloudfs.File, bool) {
				return cloudfs.NewFile(filepath.Join(prefix, file.Path()), file), true
			},
		}
		return newHookFS(fs, opt), nil
	}
}
