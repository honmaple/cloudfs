package structutil

import (
	"reflect"

	"github.com/spf13/viper"
)

type Filter struct {
	*viper.Viper
}

func NewFilter(m map[string]string) *Filter {
	cf := viper.New()
	for k, v := range m {
		cf.Set(k, v)
	}
	return &Filter{cf}
}

type replaceTag[T any] struct {
	newModel any
	oldModel T
	isset    bool
	values   map[string]reflect.Value
}

func (m *replaceTag[T]) Model() T {
	if m.isset {
		return m.oldModel
	}
	v := reflect.ValueOf(m.newModel)
	for v.Kind() == reflect.Ptr {
		v = v.Elem()
	}

	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		if oldVal, ok := m.values[field.Name]; ok {
			oldVal.Set(v.Field(i))
		}
	}
	m.isset = true
	return m.oldModel
}

func (m *replaceTag[T]) NewModel() any {
	return m.newModel
}

func ReplaceStructTag[T any](oldModel T, replacer func(reflect.StructField) reflect.StructTag) *replaceTag[T] {
	m := &replaceTag[T]{oldModel: oldModel, values: make(map[string]reflect.Value)}

	v := reflect.ValueOf(oldModel)
	for v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	fields := make([]reflect.StructField, 0)

	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		if tag := replacer(field); tag != "" {
			field.Tag = tag
		}
		fields = append(fields, field)

		m.values[field.Name] = v.Field(i)
	}
	m.newModel = reflect.New(reflect.StructOf(fields)).Interface()
	return m
}
