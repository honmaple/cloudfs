package cloudfs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/honmaple/cloudfs/utils/structutil"
)

type (
	FS interface {
		List(context.Context, string, ...ListOption) ([]File, error)
		Move(context.Context, string, string) error
		Copy(context.Context, string, string) error
		Rename(context.Context, string, string) error
		Remove(context.Context, string) error
		MakeDir(context.Context, string) error
		Get(context.Context, string) (File, error)
		Open(string) (FileReader, error)
		Create(string) (FileWriter, error)
		Close() error
	}
	WrapFunc func(FS) (FS, error)

	Option interface {
		NewFS() (FS, error)
	}
	OptionFactory func() Option
)

type BaseFS struct{}

func (BaseFS) List(context.Context, string, ...ListOption) ([]File, error) { return nil, ErrNotSupport }
func (BaseFS) Move(context.Context, string, string) error                  { return ErrNotSupport }
func (BaseFS) Copy(context.Context, string, string) error                  { return ErrNotSupport }
func (BaseFS) Rename(context.Context, string, string) error                { return ErrNotSupport }
func (BaseFS) Remove(context.Context, string) error                        { return ErrNotSupport }
func (BaseFS) MakeDir(context.Context, string) error                       { return ErrNotSupport }
func (BaseFS) Get(context.Context, string) (File, error)                   { return nil, ErrNotSupport }
func (BaseFS) Open(string) (FileReader, error)                             { return nil, ErrNotSupport }
func (BaseFS) Create(string) (FileWriter, error)                           { return nil, ErrNotSupport }
func (BaseFS) Close() error                                                { return nil }

func New(driver string, option map[string]any, fns ...WrapFunc) (FS, error) {
	factory, ok := allFactories[driver]
	if !ok {
		return nil, fmt.Errorf("The driver %s not found", driver)
	}

	b, err := json.Marshal(option)
	if err != nil {
		return nil, err
	}

	opt := factory()
	if err := json.Unmarshal(b, opt); err != nil {
		return nil, err
	}
	return opt.NewFS()
}

func NewWithString(driver string, option string, fns ...WrapFunc) (FS, error) {
	factory, ok := allFactories[driver]
	if !ok {
		return nil, fmt.Errorf("The driver %s not found", driver)
	}

	opt := factory()
	if err := json.Unmarshal([]byte(option), opt); err != nil {
		return nil, err
	}
	return opt.NewFS()
}

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

func Verify(driver string, option string) error {
	factory, ok := allFactories[driver]
	if !ok {
		return fmt.Errorf("The driver %s not found", driver)
	}
	opt := factory()
	if err := json.Unmarshal([]byte(option), opt); err != nil {
		return err
	}
	return structutil.Verify(opt)
}

func Exists(driver string) bool {
	_, ok := allFactories[driver]
	return ok
}

func Register(typ string, factory OptionFactory) {
	allFactories[typ] = factory
}

var allFactories map[string]OptionFactory

func init() {
	allFactories = make(map[string]OptionFactory)
}
