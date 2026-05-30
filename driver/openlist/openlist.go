package openlist

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	filepath "path"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/utils/httputil"
	"github.com/honmaple/cloudfs/utils/ioutil"
	"github.com/honmaple/cloudfs/utils/structutil"
	"github.com/tidwall/gjson"
)

type Option struct {
	Endpoint string `json:"endpoint"  validate:"required"`
	Username string `json:"username"  validate:"required"`
	Password string `json:"password"  validate:"required"`
}

func (opt *Option) NewFS() (cloudfs.FS, error) {
	return New(opt)
}

type Openlist struct {
	cloudfs.BaseFS
	opt    *Option
	token  string
	client *httputil.Client
}

var _ cloudfs.FS = (*Openlist)(nil)

func (d *Openlist) request(ctx context.Context, method, url string, opts ...httputil.Option) (io.ReadCloser, error) {
	if strings.HasPrefix(url, "/") {
		url = strings.TrimSuffix(d.opt.Endpoint, "/") + url
	}

	if opts == nil {
		opts = make([]httputil.Option, 0)
	}

	opts = append(opts, httputil.WithContext(ctx))
	if d.token != "" {
		opts = append(opts, httputil.WithHeaders(map[string]string{
			"Authorization": d.token,
		}))
	}

	resp, err := d.client.Request(method, url, opts...)
	if err != nil {
		return nil, err
	}

	if code := resp.StatusCode; code == http.StatusPartialContent || code == http.StatusOK {
		return resp.Body, nil
	}
	resp.Body.Close()
	return nil, fmt.Errorf("bad status: %d", resp.StatusCode)
}

func (d *Openlist) requestWithData(ctx context.Context, method, url string, data map[string]any) ([]byte, error) {
	r, err := d.request(ctx, method, url, httputil.WithJson(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()

	return io.ReadAll(r)
}

func (d *Openlist) List(ctx context.Context, path string, opts ...cloudfs.ListOption) ([]cloudfs.File, error) {
	meta := cloudfs.ListOptions(opts...)

	resp, err := d.requestWithData(ctx, http.MethodPost, "/api/fs/list", map[string]any{
		"page":     1,
		"per_page": 0,
		"path":     path,
		"password": meta.GetString("password"),
		"refresh":  false,
	})
	if err != nil {
		return nil, err
	}
	results := gjson.ParseBytes(resp).Get("data.content").Array()

	files := make([]cloudfs.File, len(results))
	for i, result := range results {
		files[i] = cloudfs.NewFile(path, &fileinfo{result})
	}
	return files, nil
}

func (d *Openlist) Rename(ctx context.Context, path, newName string) error {
	_, err := d.requestWithData(ctx, http.MethodPost, "/api/fs/rename", map[string]any{
		"path": path,
		"name": newName,
	})
	return err
}

func (d *Openlist) Move(ctx context.Context, src, dst string) error {
	_, err := d.requestWithData(ctx, http.MethodPost, "/api/fs/move", map[string]any{
		"src_dir": filepath.Dir(src),
		"dst_dir": dst,
		"names":   []string{filepath.Base(src)},
	})
	return err
}

func (d *Openlist) Copy(ctx context.Context, src, dst string) error {
	_, err := d.requestWithData(ctx, http.MethodPost, "/api/fs/copy", map[string]any{
		"src_dir": filepath.Dir(src),
		"dst_dir": dst,
		"names":   []string{filepath.Base(src)},
	})
	return err
}

func (d *Openlist) Remove(ctx context.Context, path string) error {
	_, err := d.requestWithData(ctx, http.MethodPost, "/api/fs/remove", map[string]any{
		"dir":   filepath.Dir(path),
		"names": []string{filepath.Base(path)},
	})
	return err
}

func (d *Openlist) MakeDir(ctx context.Context, path string) error {
	_, err := d.requestWithData(ctx, http.MethodPost, "/api/fs/mkdir", map[string]any{
		"path": path,
	})
	return err
}

func (d *Openlist) Get(ctx context.Context, path string) (cloudfs.File, error) {
	resp, err := d.requestWithData(ctx, http.MethodPost, "/api/fs/get", map[string]any{
		"path":     path,
		"password": "",
	})
	if err != nil {
		return nil, err
	}
	result := gjson.ParseBytes(resp)
	if result.Get("code").Int() != 200 {
		msg := result.Get("message").String()
		if strings.Contains(msg, "not found") {
			return nil, os.ErrNotExist
		}
		return nil, errors.New(msg)
	}
	return cloudfs.NewFile(filepath.Dir(path), &fileinfo{result.Get("data")}), nil
}

func (d *Openlist) Open(path string) (cloudfs.FileReader, error) {
	resp, err := d.requestWithData(context.Background(), http.MethodPost, "/api/fs/get", map[string]any{
		"path":     path,
		"password": "",
	})
	if err != nil {
		return nil, err
	}
	result := gjson.ParseBytes(resp)

	url := result.Get("data.raw_url").String()
	if url == "" {
		return nil, fmt.Errorf("can't open %s", path)
	}

	rangeFunc := func(offset, length int64) (io.ReadCloser, error) {
		return d.request(context.Background(), http.MethodGet, url, httputil.WithNeverTimeout(), httputil.WithRequest(func(req *http.Request) {
			if length > 0 {
				req.Header.Add("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
			} else {
				req.Header.Add("Range", fmt.Sprintf("bytes=%d-", offset))
			}
		}))
	}
	return cloudfs.NewFileReader(result.Get("data.size").Int(), rangeFunc)
}

func (d *Openlist) Create(path string) (cloudfs.FileWriter, error) {
	r, w := ioutil.Pipe()
	go func() {
		resp, err := d.request(context.Background(), http.MethodPut, "/api/fs/put", httputil.WithBody(r), httputil.WithNeverTimeout(), httputil.WithRequest(func(req *http.Request) {
			req.Header.Set("File-Path", path)
			req.Header.Set("Password", "")
		}))
		if err != nil {
			r.CloseWithError(err)
			return
		}
		defer resp.Close()
		r.Close()
	}()
	return w, nil
}

func (d *Openlist) login() error {
	ctx := context.Background()
	if d.opt.Username == "" || d.opt.Password == "" {
		resp, err := d.requestWithData(ctx, http.MethodGet, "/api/me", nil)
		if err != nil {
			return err
		}
		result := gjson.ParseBytes(resp)
		if result.Get("code").Int() == 401 {
			return errors.New("游客无法访问")
		}
		return nil
	}
	resp, err := d.requestWithData(ctx, http.MethodPost, "/api/auth/login", map[string]any{
		"username": d.opt.Username,
		"password": d.opt.Password,
	})
	if err != nil {
		return err
	}
	d.token = gjson.ParseBytes(resp).Get("data.token").String()
	if d.token == "" {
		return errors.New("登录错误，无法获取Token")
	}
	return nil
}

func New(opt *Option) (cloudfs.FS, error) {
	if err := structutil.Verify(opt); err != nil {
		return nil, err
	}

	d := &Openlist{
		opt:    opt,
		client: httputil.New(),
	}

	if err := d.login(); err != nil {
		return nil, err
	}
	return d, nil
}

func init() {
	cloudfs.Register("openlist", func() cloudfs.Option {
		return &Option{}
	})
}
