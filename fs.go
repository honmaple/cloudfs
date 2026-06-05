package cloudfs

import (
	"context"
)

type (
	FS interface {
		List(context.Context, string, ...ListOption) ([]FileInfo, error)
		Move(context.Context, string, string) error
		Copy(context.Context, string, string) error
		Rename(context.Context, string, string) error
		Remove(context.Context, string) error
		MakeDir(context.Context, string) error
		Stat(context.Context, string) (FileInfo, error)
		Open(context.Context, string) (File, error)
		Create(context.Context, string) (FileWriter, error)
		Close() error
	}
	WrapFunc func(FS) (FS, error)
)

func WrapFS(fs FS, fns ...WrapFunc) (FS, error) {
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

type BaseFS struct{}

func (BaseFS) List(context.Context, string, ...ListOption) ([]FileInfo, error) {
	return nil, ErrNotSupport
}
func (BaseFS) Move(context.Context, string, string) error         { return ErrNotSupport }
func (BaseFS) Copy(context.Context, string, string) error         { return ErrNotSupport }
func (BaseFS) Rename(context.Context, string, string) error       { return ErrNotSupport }
func (BaseFS) Remove(context.Context, string) error               { return ErrNotSupport }
func (BaseFS) MakeDir(context.Context, string) error              { return ErrNotSupport }
func (BaseFS) Stat(context.Context, string) (FileInfo, error)     { return nil, ErrNotSupport }
func (BaseFS) Open(context.Context, string) (File, error)         { return nil, ErrNotSupport }
func (BaseFS) Create(context.Context, string) (FileWriter, error) { return nil, ErrNotSupport }
func (BaseFS) Close() error                                       { return nil }
