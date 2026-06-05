package middleware

import (
	"github.com/honmaple/cloudfs"
)

var (
	NewFS = cloudfs.New
)

type (
	Option interface {
		NewFS(cloudfs.FS) (cloudfs.FS, error)
	}
)

func OptionFS(opt Option) cloudfs.WrapFunc {
	return opt.NewFS
}
