package cloudfs

import (
	"context"
	"crypto"
)

type (
	// FS uses slash-separated paths rooted at /. Drivers adapt paths to their backend.
	FS interface {
		List(context.Context, string) ([]FileInfo, error)
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
	// Hasher is an optional FS capability for retrieving content hashes.
	// Hash returns a lowercase hexadecimal digest for the requested algorithm.
	// An unavailable hash returns an empty string or ErrNotSupport. Implementations
	// should use backend metadata rather than download the file to calculate it.
	Hasher interface {
		Hash(context.Context, string, crypto.Hash) (string, error)
		// Hashes returns the algorithms supported by this filesystem, in preference order.
		Hashes() []crypto.Hash
	}
	WrapFunc func(FS) (FS, error)
)

type BaseFS struct{}

func (BaseFS) List(context.Context, string) ([]FileInfo, error) {
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

func New(fs FS, fns ...WrapFunc) (FS, error) {
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
