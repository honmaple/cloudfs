package pan115

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	filepath "path"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/driver/quark"
	"github.com/honmaple/cloudfs/utils/httputil"
	"github.com/honmaple/cloudfs/utils/structutil"
)

const (
	ua = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_13_5, AppleWebKit/605.1.15 (KHTML, like Gecko,"
)

type Option struct {
	Cookie string `json:"cookie"  validate:"required"`
}

func (opt *Option) NewFS() (cloudfs.FS, error) {
	return New(opt)
}

type Pan115 struct {
	cloudfs.BaseFS
	opt        *Option
	token      string
	client     *driver115.Pan115Client
	httpClient *httputil.Client
}

var _ cloudfs.FS = (*Pan115)(nil)

func (d *Pan115) List(ctx context.Context, path string, opts ...cloudfs.ListOption) ([]cloudfs.FileInfo, error) {
	meta := cloudfs.ListOptions(opts...)

	limit := meta.GetInt64("page_size")
	if limit == 0 {
		limit = 50
	}
	results, err := d.client.ListPage(path, meta.GetInt64("offset"), limit)
	if err != nil {
		return nil, err
	}

	files := make([]cloudfs.FileInfo, 0)
	for _, result := range *results {
		info := (&cloudfs.Entry{
			Name:    result.Name,
			Size:    result.Size,
			Path:    path,
			IsDir:   result.IsDirectory,
			ModTime: result.UpdateTime,
			ExtraInfo: map[string]any{
				"id":        result.FileID,
				"pick_code": result.PickCode,
			},
			Sys: result,
		}).FileInfo()
		files = append(files, info)
	}
	return files, nil
}

func (d *Pan115) Rename(ctx context.Context, path, newName string) error {
	return d.client.Rename(path, newName)
}

func (d *Pan115) Move(ctx context.Context, src, dst string) error {
	return d.client.Move(dst, src)
}

func (d *Pan115) Copy(ctx context.Context, src, dst string) error {
	return d.client.Copy(dst, src)
}

func (d *Pan115) MakeDir(ctx context.Context, path string) error {
	_, err := d.client.Mkdir(filepath.Dir(path), filepath.Base(path))
	return err
}

func (d *Pan115) Remove(ctx context.Context, path string) error {
	return d.client.Delete(path)
}

func (d *Pan115) Stat(ctx context.Context, path string) (cloudfs.FileInfo, error) {
	result, err := d.client.GetFile(path)
	if err != nil {
		return nil, err
	}
	info := (&cloudfs.Entry{
		Name:    result.Name,
		Size:    result.Size,
		Path:    path,
		IsDir:   result.IsDirectory,
		ModTime: result.UpdateTime,
		ExtraInfo: map[string]any{
			"id": result.FileID,
		},
		Sys: result,
	}).FileInfo()
	return info, nil
}

func (d *Pan115) Open(ctx context.Context, path string) (cloudfs.File, error) {
	result, err := d.client.GetFile(path)
	if err != nil {
		return nil, err
	}

	info, err := d.client.DownloadWithUA(result.PickCode, ua)
	if err != nil {
		return nil, err
	}

	rangeFunc := func(offset, length int64) (io.ReadCloser, error) {
		opts := []httputil.Option{
			httputil.WithContext(ctx),
			httputil.WithNeverTimeout(),
			httputil.WithRequest(func(req *http.Request) {
				if length > 0 {
					req.Header.Add("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
				} else {
					req.Header.Add("Range", fmt.Sprintf("bytes=%d-", offset))
				}
				for key, values := range info.Header {
					for _, value := range values {
						req.Header.Add(key, value)
					}
				}
			}),
		}
		resp, err := d.httpClient.Request(http.MethodGet, info.Url.Url, opts...)
		if err != nil {
			return nil, err
		}
		if code := resp.StatusCode; code == http.StatusPartialContent || code == http.StatusOK {
			return resp.Body, nil
		}
		resp.Body.Close()
		return nil, fmt.Errorf("bad status: %d", resp.StatusCode)
	}
	return cloudfs.NewFile(result.Size, rangeFunc)
}

func New(opt *Option) (cloudfs.FS, error) {
	if err := structutil.Verify(opt); err != nil {
		return nil, err
	}

	cr := &driver115.Credential{}
	if err := cr.FromCookie(opt.Cookie); err != nil {
		return nil, errors.New("invalid cookies")
	}

	client := driver115.Defalut().ImportCredential(cr)
	if err := client.LoginCheck(); err != nil {
		return nil, err
	}

	d := &Pan115{
		opt:        opt,
		client:     client,
		httpClient: httputil.New(),
	}
	return quark.WrapFS(d, 180), nil
}

func init() {
	cloudfs.Register("pan115", func() cloudfs.Option {
		return &Option{}
	})
}
