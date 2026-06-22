package cloudfs

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/viper"
)

type (
	Query struct {
		*viper.Viper
	}
)

func NewQuery(opts ...map[string]any) *Query {
	m := &Query{Viper: viper.New()}
	for _, opt := range opts {
		for key, value := range opt {
			m.Set(key, value)
		}
	}
	return m
}

func ParsePath(path string) (string, *Query) {
	m := &Query{Viper: viper.New()}
	if path == "" {
		return path, m
	}
	i := strings.Index(path, "?")
	if i < 0 {
		return path, m
	}
	values, err := url.ParseQuery(path[i+1:])
	if err == nil {
		for key, value := range values {
			if len(value) == 1 {
				m.Set(key, value[0])
			} else {
				m.Set(key, value)
			}
		}
	}
	return path[:i], m
}

func PathWithQuery(path string, query *Query) string {
	if query == nil {
		return path
	}
	return PathWithValues(path, query.AllSettings())
}

func PathWithValues(path string, values map[string]any) string {
	if len(values) == 0 {
		return path
	}
	query := url.Values{}
	for key, value := range values {
		switch v := value.(type) {
		case []string:
			for _, item := range v {
				query.Add(key, item)
			}
		case []any:
			for _, item := range v {
				query.Add(key, fmt.Sprint(item))
			}
		default:
			query.Set(key, fmt.Sprint(v))
		}
	}
	rawQuery := query.Encode()
	if rawQuery == "" {
		return path
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
		if strings.HasSuffix(path, "?") || strings.HasSuffix(path, "&") {
			sep = ""
		}
	}
	return path + sep + rawQuery
}
