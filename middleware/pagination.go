package middleware

import (
	"context"

	"github.com/honmaple/cloudfs"
)

type PaginationOption struct {
	PageKey     string `json:"page_key"`
	PageSizeKey string `json:"page_size_key"`
}

func (opt *PaginationOption) NewFS(fs cloudfs.FS) (cloudfs.FS, error) {
	return newPaginationFS(fs, opt)
}

type paginationFS struct {
	cloudfs.FS
	opt *PaginationOption
}

var _ cloudfs.FS = (*paginationFS)(nil)

func (d *paginationFS) List(ctx context.Context, path string) ([]cloudfs.FileInfo, error) {
	cleanPath, query := cloudfs.ParsePath(path)
	pageSize := query.GetInt(d.opt.PageSizeKey)
	if pageSize <= 0 {
		return d.FS.List(ctx, path)
	}

	values := query.AllSettings()
	delete(values, d.opt.PageKey)
	delete(values, d.opt.PageSizeKey)

	files, err := d.FS.List(ctx, cloudfs.PathWithValues(cleanPath, values))
	if err != nil {
		return nil, err
	}

	page := query.GetInt(d.opt.PageKey)
	if page <= 0 {
		page = 1
	}

	start := (page - 1) * pageSize
	if start >= len(files) {
		return []cloudfs.FileInfo{}, nil
	}

	end := start + pageSize
	if end > len(files) {
		end = len(files)
	}
	return files[start:end], nil
}

func newPaginationFS(fs cloudfs.FS, opt *PaginationOption) (cloudfs.FS, error) {
	if opt == nil {
		opt = &PaginationOption{}
	}
	if opt.PageKey == "" {
		opt.PageKey = "page"
	}
	if opt.PageSizeKey == "" {
		opt.PageSizeKey = "page_size"
	}
	return &paginationFS{FS: fs, opt: opt}, nil
}

func PaginationFS(opt *PaginationOption) cloudfs.WrapFunc {
	return opt.NewFS
}
