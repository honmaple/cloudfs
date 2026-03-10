package structutil

// import (
//	"iter"
//	"reflect"
//	"slices"
//	"strings"
// )

// // type diffItem interface {
// //	String() string
// // }

// type DiffType string

// var (
//	DiffTypeAdd DiffType = "add"
//	DiffTypeDel DiffType = "del"
// )

// type Diff[T any] struct {
//	tag    string
//	equal  func(string, any, any) bool
//	fields []string

//	oldValue T
//	newValue T
// }

// func (d *Diff[T]) valueMap(value T) map[string]reflect.Value {
//	v := reflect.ValueOf(value)
//	for v.Kind() == reflect.Ptr {
//		v = v.Elem()
//	}

//	fields := make(map[string]reflect.Value)

//	t := v.Type()
//	for i := range t.NumField() {
//		field := t.Field(i)

//		if !field.IsExported() {
//			continue
//		}

//		// if field.Type.Kind() == reflect.Struct {
//		//	continue
//		// }

//		if field.Type.Kind() == reflect.Slice {
//			// value := v.Field(i)
//			// for i := 0; i < v.Field(i).Len(); i++ {
//			//	e := value.Index(i)
//			//	// s := e.FieldByName("F")
//			//	// Do something with s
//			// }
//			continue
//		}

//		name := field.Name
//		if d.tag != "" {
//			tag := field.Tag.Get(d.tag)
//			if name == "" || name == "-" {
//				continue
//			}
//			if s := strings.Split(tag, ","); len(s) > 0 {
//				name = s[0]
//			}
//		}
//		if len(d.fields) == 0 || slices.Contains(d.fields, name) {
//			fields[name] = v.Field(i)
//		}
//	}
//	return fields
// }

// func (d *Diff[T]) Map() map[string]any {
//	m := make(map[string]any)

//	for field := range d.Fields() {

//	}
//	return m
// }

// func (d *Diff[T]) Fields() iter.Seq[DiffField[any]] {
//	oldValues := d.valueMap(d.oldValue)
//	newValues := d.valueMap(d.newValue)
//	return func(yield func(DiffField[any]) bool) {
//		for field, value := range oldValues {
//			newValue, ok := newValues[field]
//			if !ok {
//				diffField := DiffField[any]{
//					Type:  DiffTypeDel,
//					Name:  field,
//					Value: value.Interface(),
//				}
//				if !yield(diffField) {
//					return
//				}
//				continue
//			}

//			if value.Kind() == reflect.Slice {
//				fields := DiffSlice(value.Slice(0, value.Len()-1), newValue.Slice(0, newValue.Len()-1))
//			}
//			if !d.equal(field, value, newValue) {
//				diffField := DiffField[any]{
//					Type:  DiffTypeAdd,
//					Name:  field,
//					Value: value.Interface(),
//				}
//				if !yield(diffField) {
//					return
//				}
//			}
//		}
//		for field, value := range newValues {
//			_, ok := oldValues[field]
//			if !ok {
//				diffField := DiffField[any]{
//					Type:  DiffTypeAdd,
//					Name:  field,
//					Value: value.Interface(),
//				}
//				if !yield(diffField) {
//					return
//				}
//			}
//		}
//	}
// }

// type DiffOption[T any] func(*Diff[T])

// func WithDiffTag[T any](tag string) func(*Diff[T]) {
//	return func(diff *Diff[T]) {
//		diff.tag = tag
//	}
// }

// func WithDiffFields[T any](fields ...string) func(*Diff[T]) {
//	return func(diff *Diff[T]) {
//		diff.fields = fields
//	}
// }

// func NewDiff[T any](oldValue T, newValue T, opts ...DiffOption[T]) *Diff[T] {
//	d := &Diff[T]{
//		oldValue: oldValue,
//		newValue: newValue,
//	}
//	for _, opt := range opts {
//		opt(d)
//	}
//	return d
// }

// type DiffField[T any] struct {
//	Type  DiffType
//	Name  string
//	Value T
// }

// func DiffSlice[T any](oldValues []T, newValues []T, compare func(T, T) bool) ([]T, []T) {
//	fields := make([]DiffField[T], 0)

//	for _, oldValue := range oldValues {
//		found := false
//		for _, newValue := range newValues {
//			if compare(oldValue, newValue) {
//				found = true
//				break
//			}
//		}
//		if !found {
//			fields = append(fields, DiffField[T]{
//				Type:  DiffTypeDel,
//				Value: oldValue,
//			})
//		}
//	}

//	for _, newValue := range newValues {
//		found := false
//		for _, oldValue := range oldValues {
//			if compare(oldValue, newValue) {
//				found = true
//				break
//			}
//		}
//		if !found {
//			fields = append(fields, DiffField[T]{
//				Type:  DiffTypeAdd,
//				Value: newValue,
//			})
//		}
//	}
//	return fields
// }
