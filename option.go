package cloudfs

import (
	"github.com/spf13/viper"
)

type (
	Option struct {
		*viper.Viper
	}
	ListOption map[string]any
)

func ListOptions(opts ...ListOption) *Option {
	m := &Option{viper.New()}
	for _, opt := range opts {
		for k, v := range opt {
			m.Set(k, v)
		}
	}
	return m
}

func NewOption(opts ...map[string]any) *Option {
	m := &Option{viper.New()}
	for _, opt := range opts {
		for k, v := range opt {
			m.Set(k, v)
		}
	}
	return m
}
