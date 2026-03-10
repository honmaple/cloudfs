package middleware

import (
	"context"
	"errors"
	"time"

	"github.com/honmaple/cloudfs"
	"golang.org/x/time/rate"
)

var (
	errRateLimit = errors.New("访问频率限制")
)

type RateLimitOption struct {
	Wait  bool          `json:"wait"`
	Burst int           `json:"burst"`
	Limit time.Duration `json:"limit"`
}

func (opt *RateLimitOption) NewFS(fs cloudfs.FS) (cloudfs.FS, error) {
	return newRateLimitFS(fs, opt)
}

type rateLimiteFS struct {
	cloudfs.FS
	opt     *RateLimitOption
	limiter *rate.Limiter
}

var _ cloudfs.FS = (*rateLimiteFS)(nil)

func (d *rateLimiteFS) checkLimit(ctx context.Context) error {
	if !d.limiter.Allow() {
		if !d.opt.Wait {
			return errRateLimit
		}
		if err := d.limiter.Wait(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (d *rateLimiteFS) List(ctx context.Context, path string, opts ...cloudfs.ListOption) ([]cloudfs.File, error) {
	if err := d.checkLimit(ctx); err != nil {
		return nil, err
	}
	return d.FS.List(ctx, path, opts...)
}

func (d *rateLimiteFS) Copy(ctx context.Context, src, dst string) error {
	if err := d.checkLimit(ctx); err != nil {
		return err
	}
	return d.FS.Copy(ctx, src, dst)
}

func (d *rateLimiteFS) Move(ctx context.Context, src, dst string) error {
	if err := d.checkLimit(ctx); err != nil {
		return err
	}
	return d.FS.Move(ctx, src, dst)
}

func (d *rateLimiteFS) Rename(ctx context.Context, src, dst string) error {
	if err := d.checkLimit(ctx); err != nil {
		return err
	}
	return d.FS.Rename(ctx, src, dst)
}

func (d *rateLimiteFS) Remove(ctx context.Context, path string) error {
	if err := d.checkLimit(ctx); err != nil {
		return err
	}
	return d.FS.Remove(ctx, path)
}

func (d *rateLimiteFS) MakeDir(ctx context.Context, path string) error {
	if err := d.checkLimit(ctx); err != nil {
		return err
	}
	return d.FS.MakeDir(ctx, path)
}

func (d *rateLimiteFS) Get(ctx context.Context, path string) (cloudfs.File, error) {
	if err := d.checkLimit(ctx); err != nil {
		return nil, err
	}
	return d.FS.Get(ctx, path)
}

func (d *rateLimiteFS) Open(path string) (cloudfs.FileReader, error) {
	if err := d.checkLimit(context.TODO()); err != nil {
		return nil, err
	}
	return d.FS.Open(path)
}

func (d *rateLimiteFS) Create(path string) (cloudfs.FileWriter, error) {
	if err := d.checkLimit(context.TODO()); err != nil {
		return nil, err
	}
	return d.FS.Create(path)
}

// 访问频率限制
func newRateLimitFS(fs cloudfs.FS, opt *RateLimitOption) (cloudfs.FS, error) {
	// 默认为 100/秒
	if opt.Burst <= 0 {
		opt.Burst = 100
	}
	if opt.Limit <= 0 {
		opt.Limit = 1
	}
	return &rateLimiteFS{
		FS:      fs,
		opt:     opt,
		limiter: rate.NewLimiter(rate.Every(time.Second*opt.Limit), opt.Burst),
	}, nil
}

func RateLimitFS(opt *RateLimitOption) cloudfs.WrapFunc {
	return opt.NewFS
}
