package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	filepath "path"

	"github.com/google/go-github/v70/github"
	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/driver"
	"github.com/honmaple/cloudfs/utils/httputil"
	"github.com/honmaple/cloudfs/utils/structutil"
)

type Option struct {
	Ref        string `json:"ref"`
	Repo       string `json:"repo"`
	Owner      string `json:"owner"  validate:"required"`
	Token      string `json:"token"`
	ShowTag    bool   `json:"show_tag"`
	ShowBranch bool   `json:"show_branch"`
}

func (opt *Option) NewFS() (cloudfs.FS, error) {
	return New(opt)
}

type Github struct {
	cloudfs.BaseFS
	opt        *Option
	client     *github.Client
	httpClient *httputil.Client
}

var _ cloudfs.FS = (*Github)(nil)

func (d *Github) request(ctx context.Context, method, url string, opts ...httputil.Option) (io.ReadCloser, error) {
	if opts == nil {
		opts = make([]httputil.Option, 0)
	}

	opts = append(opts, httputil.WithContext(ctx))

	resp, err := d.httpClient.Request(method, url, opts...)
	if err != nil {
		return nil, err
	}

	if code := resp.StatusCode; code == http.StatusPartialContent || code == http.StatusOK {
		return resp.Body, nil
	}
	resp.Body.Close()
	return nil, fmt.Errorf("bad status: %d", resp.StatusCode)
}

func (d *Github) download(ctx context.Context, url string, size int64) (cloudfs.File, error) {
	if url == "" {
		return nil, errors.New("no download url")
	}
	rangeFunc := func(offset, length int64) (io.ReadCloser, error) {
		return d.request(ctx, http.MethodGet, url, httputil.WithNeverTimeout(), httputil.WithRequest(func(req *http.Request) {
			if length > 0 {
				req.Header.Add("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
			} else {
				req.Header.Add("Range", fmt.Sprintf("bytes=%d-", offset))
			}
		}))
	}
	return cloudfs.NewFile(size, rangeFunc)
}

func (d *Github) splitPath(path string) (string, string) {
	path = path[1:]
	i := strings.IndexByte(path, '/')
	if i == -1 {
		return path, "/"
	}
	return path[:i], path[i:]
}

// /{user}/{repo}/{ref}/readme.md
func (d *Github) getActualPath(path string) (string, string, string) {
	if path == "/" {
		return d.opt.Repo, d.opt.Ref, path
	}

	repo := d.opt.Repo
	if repo == "" {
		repo, path = d.splitPath(path)
	}

	ref := d.opt.Ref
	if ref == "" && (d.opt.ShowTag || d.opt.ShowBranch) {
		ref, path = d.splitPath(path)
		if b, err := url.PathUnescape(ref); err == nil {
			ref = b
		}
	}
	return repo, ref, path
}

func newFileInfo(path, name string, size int64, isDir bool, modTime time.Time) cloudfs.FileInfo {
	return (&cloudfs.Entry{
		Name:    name,
		Size:    size,
		Path:    path,
		IsDir:   isDir,
		ModTime: modTime,
	}).FileInfo()
}

func (d *Github) Stat(ctx context.Context, path string) (cloudfs.FileInfo, error) {
	repo, ref, actualPath := d.getActualPath(path)
	if repo == "" {
		return nil, fmt.Errorf("can't stat %s", path)
	}

	// 获取仓库信息
	if ref == "" && actualPath == "/" {
		result, _, err := d.client.Repositories.Get(ctx, d.opt.Owner, repo)
		if err != nil {
			return nil, err
		}

		return newFileInfo(path, result.GetName(), int64(result.GetSize()), true, result.GetUpdatedAt().Time), nil
	}

	// 获取分支信息
	if actualPath == "/" {
		return newFileInfo(path, ref, 0, true, time.Now()), nil
	}

	dir, filename := filepath.Dir(actualPath), filepath.Base(actualPath)
	_, dc, _, err := d.client.Repositories.GetContents(ctx, d.opt.Owner, repo, dir, &github.RepositoryContentGetOptions{
		Ref: ref,
	})
	if err != nil {
		return nil, err
	}

	for _, result := range dc {
		if result.GetName() != filename {
			continue
		}
		return newFileInfo(path, result.GetName(), int64(result.GetSize()), result.GetType() == "dir", time.Now()), nil
	}
	return nil, fmt.Errorf("no file named %s found in %s", filename, dir)
}

func (d *Github) Open(ctx context.Context, path string) (cloudfs.File, error) {
	repo, ref, actualPath := d.getActualPath(path)
	if repo == "" || actualPath == "/" {
		return nil, fmt.Errorf("can't open %s", path)
	}

	dir, filename := filepath.Dir(actualPath), filepath.Base(actualPath)
	_, dc, _, err := d.client.Repositories.GetContents(ctx, d.opt.Owner, repo, dir, &github.RepositoryContentGetOptions{
		Ref: ref,
	})
	if err != nil {
		return nil, err
	}

	for _, result := range dc {
		if result.GetName() != filename {
			continue
		}
		return d.download(ctx, result.GetDownloadURL(), int64(result.GetSize()))
	}
	return nil, fmt.Errorf("no file named %s found in %s", filename, dir)
}

func (d *Github) List(ctx context.Context, path string, opts ...cloudfs.ListOption) ([]cloudfs.FileInfo, error) {
	repo, ref, actualPath := d.getActualPath(path)

	if repo == "" {
		files := make([]cloudfs.FileInfo, 0)
		for i := 1; ; i++ {
			opts := &github.RepositoryListByUserOptions{
				ListOptions: github.ListOptions{
					Page:    i,
					PerPage: 100,
				},
			}
			results, _, err := d.client.Repositories.ListByUser(ctx, d.opt.Owner, opts)
			if err != nil {
				return nil, err
			}
			for _, result := range results {
				files = append(files, newFileInfo(path, result.GetName(), int64(result.GetSize()), true, result.GetUpdatedAt().Time))
			}
			if len(results) < 100 {
				break
			}
		}
		return files, nil
	}

	if ref == "" && (d.opt.ShowBranch || d.opt.ShowTag) {
		files := make([]cloudfs.FileInfo, 0)

		if d.opt.ShowBranch {
			for i := 1; ; i++ {
				opts := &github.BranchListOptions{
					ListOptions: github.ListOptions{
						Page:    i,
						PerPage: 100,
					},
				}
				results, _, err := d.client.Repositories.ListBranches(ctx, d.opt.Owner, repo, opts)
				if err != nil {
					return nil, err
				}
				for _, result := range results {
					// 分支名称可能包括路径分隔符
					files = append(files, newFileInfo(path, url.PathEscape(result.GetName()), 0, true, time.Now()))
				}
				if len(results) < 100 {
					break
				}
			}
		}

		if d.opt.ShowTag {
			for i := 1; ; i++ {
				opts := &github.ListOptions{
					Page:    i,
					PerPage: 100,
				}
				results, _, err := d.client.Repositories.ListTags(ctx, d.opt.Owner, repo, opts)
				if err != nil {
					return nil, err
				}
				for _, result := range results {
					files = append(files, newFileInfo(path, url.PathEscape(result.GetName()), 0, true, time.Now()))
				}
				if len(results) < 100 {
					break
				}
			}
		}
		return files, nil
	}

	copts := &github.RepositoryContentGetOptions{
		Ref: ref,
	}

	fc, dc, _, err := d.client.Repositories.GetContents(ctx, d.opt.Owner, repo, actualPath, copts)
	if err != nil {
		return nil, err
	}
	if fc != nil {
		return []cloudfs.FileInfo{newFileInfo(path, fc.GetName(), int64(fc.GetSize()), fc.GetType() == "dir", time.Now())}, nil
	}

	files := make([]cloudfs.FileInfo, len(dc))
	for i, result := range dc {
		files[i] = newFileInfo(path, result.GetName(), int64(result.GetSize()), result.GetType() == "dir", time.Now())
	}
	return files, nil
}

func New(opt *Option) (cloudfs.FS, error) {
	if err := structutil.Verify(opt); err != nil {
		return nil, err
	}

	client := github.NewClient(nil)
	if opt.Token != "" {
		client = client.WithAuthToken(opt.Token)
	}

	d := &Github{
		opt:        opt,
		client:     client,
		httpClient: httputil.New(),
	}
	return d, nil
}

func init() {
	driver.Register("github", func() driver.Option {
		return &Option{}
	})
}
