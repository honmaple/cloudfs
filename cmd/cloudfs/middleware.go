package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/middleware"
)

type middlewareConfig struct {
	Type   string          `json:"type"`
	Option json.RawMessage `json:"option,omitempty"`
}

// wrapper validates configuration without opening the underlying driver.
func (c middlewareConfig) wrapper() (cloudfs.WrapFunc, error) {
	var option middleware.Option
	var prefix struct {
		Prefix string `json:"prefix"`
	}
	switch c.Type {
	case "prefix", "trim_prefix":
	case "cache":
		option = &middleware.CacheOption{}
	case "pagination":
		option = &middleware.PaginationOption{}
	case "rate_limit":
		option = &middleware.RateLimitOption{}
	case "compress":
		option = &middleware.CompressOption{}
	case "encrypt":
		option = &middleware.EncryptOption{}
	default:
		return nil, fmt.Errorf("unknown middleware type %q", c.Type)
	}
	data := c.Option
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	if len(data) == 0 || data[0] != '{' {
		return nil, errors.New("middleware option must be an object")
	}
	var target any = option
	if option == nil {
		target = &prefix
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return nil, errors.New("invalid middleware options")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("invalid middleware options")
	}
	switch c.Type {
	case "prefix", "trim_prefix":
		if !strings.HasPrefix(prefix.Prefix, "/") {
			return nil, errors.New("middleware prefix must start with /")
		}
		if c.Type == "prefix" {
			return middleware.PrefixFS(prefix.Prefix), nil
		}
		return func(fsys cloudfs.FS) (cloudfs.FS, error) { return middleware.TrimPrefixFS(fsys, prefix.Prefix)(fsys) }, nil
	case "encrypt":
		if option.(*middleware.EncryptOption).Password == "" {
			return nil, errors.New("encrypt middleware requires password")
		}
	case "compress":
		level := option.(*middleware.CompressOption).Level
		if level < -2 || level > 9 {
			return nil, errors.New("compress level must be between -2 and 9")
		}
	}
	return middleware.OptionFS(option), nil
}
