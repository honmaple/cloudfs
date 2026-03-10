package middleware

import (
	"github.com/honmaple/cloudfs"
)

type (
	Option interface {
		NewFS(cloudfs.FS) (cloudfs.FS, error)
	}
)

func NewFS(fs cloudfs.FS, fns ...cloudfs.WrapFunc) (cloudfs.FS, error) {
	var (
		newFS = fs
		err   error
	)
	for _, fn := range fns {
		newFS, err = fn(newFS)
		if err != nil {
			return nil, err
		}
	}
	return newFS, nil
}

func OptionFS(opt Option) cloudfs.WrapFunc {
	return opt.NewFS
}
