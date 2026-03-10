package cloudfs

import (
	"github.com/spf13/viper"
)

type meta struct {
	*viper.Viper
}

type (
	ListOption map[string]any
	CopyOption map[string]any
)

func Meta(opts ...map[string]any) *meta {
	m := &meta{viper.New()}
	for _, opt := range opts {
		for k, v := range opt {
			m.Set(k, v)
		}
	}
	return m
}
