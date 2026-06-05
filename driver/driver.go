package driver

import (
	"encoding/json"
	"fmt"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/utils/structutil"
)

type (
	Option interface {
		NewFS() (cloudfs.FS, error)
	}
	OptionFactory func() Option
)

func New(driver string, option map[string]any, fns ...cloudfs.WrapFunc) (cloudfs.FS, error) {
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

func NewFromString(driver string, option string, fns ...cloudfs.WrapFunc) (cloudfs.FS, error) {
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

func VerifyOption(driver string, option string) error {
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
